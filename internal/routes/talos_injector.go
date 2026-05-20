package routes

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/go-logr/logr"
	"github.com/siderolabs/gen/value"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosclientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	netcfg "github.com/siderolabs/talos/pkg/machinery/config/types/network"
	"github.com/siderolabs/talos/pkg/machinery/nethelpers"
	configres "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/network"
	"go.yaml.in/yaml/v4"
	"google.golang.org/protobuf/types/known/durationpb"
)

type TalosInjector struct {
	logger logr.Logger
}

func NewTalosInjector(logger logr.Logger) *TalosInjector {
	return &TalosInjector{logger: logger}
}

func (i *TalosInjector) EnsureRoutes(ctx context.Context, node Node, talosConfig []byte, staticRoutes []Route, defaultRoute *Route) error {
	if len(talosConfig) == 0 {
		return fmt.Errorf("talosconfig is empty")
	}
	if node.Address == "" {
		return fmt.Errorf("node address is empty")
	}
	if node.MachineKey == "" {
		return fmt.Errorf("node machineKey is empty")
	}

	cfg, err := talosclientconfig.FromBytes(talosConfig)
	if err != nil {
		return fmt.Errorf("parse talosconfig: %w", err)
	}

	endpoint := node.Address
	if node.Port != 0 {
		endpoint = net.JoinHostPort(node.Address, strconv.FormatInt(node.Port, 10))
	}

	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	client, err := talosclient.New(callCtx, talosclient.WithConfig(cfg), talosclient.WithEndpoints(endpoint))
	if err != nil {
		return fmt.Errorf("create talos client for %s: %w", endpoint, err)
	}
	defer func() { _ = client.Close() }()

	mcRes, err := client.COSI.Get(callCtx, resource.NewMetadata(configres.NamespaceName, configres.MachineConfigType, configres.ActiveID, resource.VersionUndefined))
	if err != nil {
		return fmt.Errorf("get active machine config: %w", err)
	}

	body, err := extractMachineConfigBody(mcRes)
	if err != nil {
		return fmt.Errorf("extract machine config: %w", err)
	}

	if len(staticRoutes) > 0 {
		if err := ensureRouteSet(callCtx, client, body, staticRoutes); err != nil {
			return err
		}
	}

	allRoutes := make([]Route, 0, len(staticRoutes)+1)
	allRoutes = append(allRoutes, staticRoutes...)
	if defaultRoute != nil {
		allRoutes = append(allRoutes, *defaultRoute)
	}

	if defaultRoute != nil {
		mcRes, err := client.COSI.Get(callCtx, resource.NewMetadata(configres.NamespaceName, configres.MachineConfigType, configres.ActiveID, resource.VersionUndefined))
		if err != nil {
			return fmt.Errorf("get active machine config: %w", err)
		}

		body, err = extractMachineConfigBody(mcRes)
		if err != nil {
			return fmt.Errorf("extract machine config: %w", err)
		}

		if err := ensureRouteSet(callCtx, client, body, allRoutes); err != nil {
			return err
		}
	}

	defaultIface := ""
	if defaultRoute != nil {
		defaultIface = defaultRoute.Interface
	}

	i.logger.Info(
		"talos linkconfig routes reconciled",
		"node", node.Address,
		"port", node.Port,
		"machineKey", node.MachineKey,
		"staticRoutes", len(staticRoutes),
		"hasDefaultRoute", defaultRoute != nil,
		"defaultRouteInterface", defaultIface,
	)

	return nil
}

func patchAndApplyMachineConfig(ctx context.Context, client *talosclient.Client, baseConfig []byte, patchBytes []byte) error {
	if len(bytes.TrimSpace(patchBytes)) == 0 {
		return nil
	}

	patch, err := configpatcher.LoadPatch(patchBytes)
	if err != nil {
		return fmt.Errorf("load linkconfig patch: %w", err)
	}

	patchedCfg, err := configpatcher.Apply(configpatcher.WithBytes(baseConfig), []configpatcher.Patch{patch})
	if err != nil {
		return fmt.Errorf("apply linkconfig patch: %w", err)
	}

	patched, err := patchedCfg.Bytes()
	if err != nil {
		return fmt.Errorf("encode patched config: %w", err)
	}

	if bytes.Equal(bytes.TrimSpace(patched), bytes.TrimSpace(baseConfig)) {
		return nil
	}

	_, err = client.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data:           patched,
		Mode:           machineapi.ApplyConfigurationRequest_NO_REBOOT,
		DryRun:         false,
		TryModeTimeout: durationpb.New(0),
	})
	if err != nil {
		return fmt.Errorf("apply machine config: %w", err)
	}

	return nil
}

func ensureRouteSet(ctx context.Context, client *talosclient.Client, machineConfig []byte, routes []Route) error {
	missingRoutes, err := missingRoutesFromMachineConfig(machineConfig, routes)
	if err != nil {
		return err
	}

	if len(missingRoutes) > 0 {
		patchBytes, err := buildLinkConfigPatch(missingRoutes)
		if err != nil {
			return err
		}

		if err := patchAndApplyMachineConfig(ctx, client, machineConfig, patchBytes); err != nil {
			return err
		}
	}

	return waitForRoutesApplied(ctx, client.COSI, routes, 30*time.Second)
}

type routeKey struct {
	iface   string
	dst     string
	gateway string
	metric  uint32
}

func missingRoutesFromMachineConfig(machineConfig []byte, desired []Route) ([]Route, error) {
	cfg, err := configloader.NewFromBytes(machineConfig)
	if err != nil {
		return nil, fmt.Errorf("parse machine config: %w", err)
	}

	existing := map[routeKey]struct{}{}

	for _, doc := range cfg.Documents() {
		lc, ok := doc.(*netcfg.LinkConfigV1Alpha1)
		if !ok {
			continue
		}

		iface := strings.TrimSpace(lc.MetaName)
		if iface == "" {
			continue
		}

		for _, r := range lc.LinkRoutes {
			dst := ""
			if r.RouteDestination.Prefix != (netip.Prefix{}) {
				dst = r.RouteDestination.String()
			}

			gw := ""
			if r.RouteGateway.Addr != (netip.Addr{}) {
				gw = r.RouteGateway.String()
			}

			existing[routeKey{iface: iface, dst: dst, gateway: gw, metric: r.RouteMetric}] = struct{}{}
		}
	}

	missing := make([]Route, 0)
	for _, r := range desired {
		iface := strings.TrimSpace(r.Interface)
		if iface == "" {
			return nil, fmt.Errorf("route interface is empty")
		}

		gwAddr, err := netip.ParseAddr(strings.TrimSpace(r.Gateway))
		if err != nil {
			return nil, fmt.Errorf("parse route gateway %q: %w", r.Gateway, err)
		}
		gw := gwAddr.String()

		dst := ""
		if strings.TrimSpace(r.Network) != "" {
			p, err := netip.ParsePrefix(strings.TrimSpace(r.Network))
			if err != nil {
				return nil, fmt.Errorf("parse route network %q: %w", r.Network, err)
			}
			dst = p.String()
		}

		key := routeKey{iface: iface, dst: dst, gateway: gw, metric: r.Metric}
		if _, ok := existing[key]; ok {
			continue
		}

		missing = append(missing, r)
	}

	return missing, nil
}

type linkConfigPatchDoc struct {
	Kind       string                 `yaml:"kind"`
	APIVersion string                 `yaml:"apiVersion"`
	Name       string                 `yaml:"name"`
	Routes     []linkConfigPatchRoute `yaml:"routes,omitempty"`
}

type linkConfigPatchRoute struct {
	Destination string `yaml:"destination,omitempty"`
	Gateway     string `yaml:"gateway,omitempty"`
	Metric      uint32 `yaml:"metric,omitempty"`
}

func buildLinkConfigPatch(routes []Route) ([]byte, error) {
	byIf := map[string][]Route{}
	for i, r := range routes {
		iface := strings.TrimSpace(r.Interface)
		if iface == "" {
			return nil, fmt.Errorf("routes[%d].interface is empty", i)
		}
		if strings.TrimSpace(r.Gateway) == "" {
			return nil, fmt.Errorf("routes[%d].gateway is empty (expected controller to derive it)", i)
		}

		byIf[iface] = append(byIf[iface], r)
	}

	if len(byIf) == 0 {
		return []byte(""), nil
	}

	ifaces := make([]string, 0, len(byIf))
	for k := range byIf {
		ifaces = append(ifaces, k)
	}
	sort.Strings(ifaces)

	var out bytes.Buffer
	for idx, iface := range ifaces {
		doc := linkConfigPatchDoc{
			Kind:       "LinkConfig",
			APIVersion: "v1alpha1",
			Name:       iface,
			Routes:     make([]linkConfigPatchRoute, 0, len(byIf[iface])),
		}

		for _, r := range byIf[iface] {
			doc.Routes = append(doc.Routes, linkConfigPatchRoute{
				Destination: r.Network,
				Gateway:     r.Gateway,
				Metric:      r.Metric,
			})
		}

		b, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if idx > 0 {
			out.WriteString("---\n")
		}
		out.Write(b)
	}

	return out.Bytes(), nil
}

func expectedMachineConfigRouteSpec(r Route) (network.RouteSpecSpec, string, error) {
	if r.Gateway == "" {
		return network.RouteSpecSpec{}, "", fmt.Errorf("route gateway is empty")
	}
	if strings.TrimSpace(r.Interface) == "" {
		return network.RouteSpecSpec{}, "", fmt.Errorf("route interface is empty")
	}

	var dst netip.Prefix
	if strings.TrimSpace(r.Network) != "" {
		parsed, err := netip.ParsePrefix(r.Network)
		if err != nil {
			return network.RouteSpecSpec{}, "", fmt.Errorf("parse route network %q: %w", r.Network, err)
		}
		dst = parsed
	}
	gw, err := netip.ParseAddr(r.Gateway)
	if err != nil {
		return network.RouteSpecSpec{}, "", fmt.Errorf("parse route gateway %q: %w", r.Gateway, err)
	}

	priority := r.Metric
	if priority == 0 {
		priority = network.DefaultRouteMetric
	}

	spec := network.RouteSpecSpec{
		Destination: dst,
		Gateway:     gw,
		OutLinkName: strings.TrimSpace(r.Interface),
		Table:       nethelpers.TableMain,
		Priority:    priority,
		Protocol:    nethelpers.ProtocolStatic,
		ConfigLayer: network.ConfigMachineConfiguration,
	}

	if !dst.IsValid() {
		if gw.Is6() {
			spec.Family = nethelpers.FamilyInet6
		} else {
			spec.Family = nethelpers.FamilyInet4
		}
	} else if dst.Addr().Is6() {
		spec.Family = nethelpers.FamilyInet6
	} else {
		spec.Family = nethelpers.FamilyInet4
	}

	if fam := spec.Normalize(); fam != 0 {
		spec.Family = fam
	}

	spec.Type = nethelpers.TypeUnicast
	if !value.IsZero(spec.Destination) && spec.Destination.Addr().IsMulticast() {
		spec.Type = nethelpers.TypeMulticast
	}

	id := network.LayeredID(spec.ConfigLayer, network.RouteID(spec.Table, spec.Family, spec.Destination, spec.Gateway, spec.Priority, spec.OutLinkName))

	return spec, id, nil
}

func waitForRoutesApplied(ctx context.Context, st state.State, routes []Route, timeout time.Duration) error {
	if len(routes) == 0 {
		return nil
	}

	deadline := time.Now().Add(timeout)

	wantIDs := make([]string, 0, len(routes))
	for _, r := range routes {
		_, id, err := expectedMachineConfigRouteSpec(r)
		if err != nil {
			return err
		}
		wantIDs = append(wantIDs, id)
	}

	for {
		missing := make([]string, 0)
		for _, id := range wantIDs {
			_, err := st.Get(ctx, resource.NewMetadata(network.ConfigNamespaceName, network.RouteSpecType, id, resource.VersionUndefined))
			if err == nil {
				continue
			}
			if state.IsNotFoundError(err) {
				missing = append(missing, id)

				continue
			}

			return fmt.Errorf("get routeSpec %s: %w", id, err)
		}

		if len(missing) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for routes to be applied (missing %d/%d): %s", len(missing), len(wantIDs), strings.Join(missing, ", "))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func extractMachineConfigBody(mc resource.Resource) ([]byte, error) {
	if mc.Metadata().Annotations().Empty() {
		return yaml.Marshal(mc.Spec())
	}

	spec, err := yaml.Marshal(mc.Spec())
	if err != nil {
		return nil, err
	}

	var bodyStr string
	if err = yaml.Unmarshal(spec, &bodyStr); err != nil {
		return nil, err
	}

	return []byte(bodyStr), nil
}
