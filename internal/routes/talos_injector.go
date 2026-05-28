// Copyright 2025 Sudo Sweden AB
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

func (i *TalosInjector) EnsureRoutes(ctx context.Context, node Node, talosConfig []byte, staticRoutes []Route, defaultRoute *Route, managedInterfaces []string) error {
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

	allRoutes := make([]Route, 0, len(staticRoutes)+1)
	allRoutes = append(allRoutes, staticRoutes...)
	if defaultRoute != nil {
		allRoutes = append(allRoutes, *defaultRoute)
	}
	managed := normalizeManagedInterfaces(managedInterfaces, allRoutes)

	if len(managed) > 0 {
		if err := ensureRouteSet(callCtx, client, body, managed, allRoutes); err != nil {
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
		"managedInterfaces", len(managed),
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

func ensureRouteSet(ctx context.Context, client *talosclient.Client, machineConfig []byte, managedInterfaces []string, routes []Route) error {
	managed := normalizeManagedInterfaces(managedInterfaces, routes)
	if len(managed) == 0 {
		return waitForRoutesApplied(ctx, client.COSI, routes, 30*time.Second)
	}

	existingByIf, err := linkConfigsFromMachineConfig(machineConfig)
	if err != nil {
		return err
	}

	deletePatchBytes, err := buildLinkConfigDeletePatch(managed, existingByIf)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(deletePatchBytes)) > 0 {
		if err := patchAndApplyMachineConfig(ctx, client, machineConfig, deletePatchBytes); err != nil {
			return err
		}

		mcRes, err := client.COSI.Get(ctx, resource.NewMetadata(configres.NamespaceName, configres.MachineConfigType, configres.ActiveID, resource.VersionUndefined))
		if err != nil {
			return fmt.Errorf("get active machine config: %w", err)
		}

		machineConfig, err = extractMachineConfigBody(mcRes)
		if err != nil {
			return fmt.Errorf("extract machine config: %w", err)
		}
	}

	replacementPatchBytes, err := buildLinkConfigPatch(managed, routes, existingByIf)
	if err != nil {
		return err
	}

	if err := patchAndApplyMachineConfig(ctx, client, machineConfig, replacementPatchBytes); err != nil {
		return err
	}

	return waitForRoutesApplied(ctx, client.COSI, routes, 30*time.Second)
}

func normalizeManagedInterfaces(managedInterfaces []string, desiredRoutes []Route) []string {
	set := map[string]struct{}{}

	for _, iface := range managedInterfaces {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		set[iface] = struct{}{}
	}

	for _, route := range desiredRoutes {
		iface := strings.TrimSpace(route.Interface)
		if iface == "" {
			continue
		}
		set[iface] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for iface := range set {
		out = append(out, iface)
	}
	sort.Strings(out)

	return out
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
	Up         *bool                  `yaml:"up,omitempty"`
	MTU        uint32                 `yaml:"mtu,omitempty"`
	Addresses  []netcfg.AddressConfig `yaml:"addresses,omitempty"`
	Multicast  *bool                  `yaml:"multicast,omitempty"`
	Routes     []linkConfigPatchRoute `yaml:"routes"`
}

type linkConfigDeletePatchDoc struct {
	Kind       string `yaml:"kind"`
	APIVersion string `yaml:"apiVersion"`
	Name       string `yaml:"name"`
	Patch      string `yaml:"$patch"`
}

type linkConfigPatchRoute struct {
	Destination string `yaml:"destination,omitempty"`
	Gateway     string `yaml:"gateway,omitempty"`
	Metric      uint32 `yaml:"metric,omitempty"`
}

func buildLinkConfigPatch(managedInterfaces []string, routes []Route, existingByIf map[string]*netcfg.LinkConfigV1Alpha1) ([]byte, error) {
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

	managed := normalizeManagedInterfaces(managedInterfaces, routes)
	if len(managed) == 0 {
		return []byte(""), nil
	}

	var out bytes.Buffer
	for idx, iface := range managed {
		desiredRoutes := byIf[iface]
		if len(desiredRoutes) == 0 {
			if _, ok := existingByIf[iface]; !ok {
				continue
			}
		}

		doc := linkConfigPatchDoc{
			Kind:       "LinkConfig",
			APIVersion: "v1alpha1",
			Name:       iface,
			Routes:     make([]linkConfigPatchRoute, 0, len(desiredRoutes)),
		}

		if existingDoc, ok := existingByIf[iface]; ok {
			doc.Up = existingDoc.LinkUp
			doc.MTU = existingDoc.LinkMTU
			doc.Multicast = existingDoc.LinkMulticast
			doc.Addresses = append([]netcfg.AddressConfig(nil), existingDoc.LinkAddresses...)
		}

		for _, r := range desiredRoutes {
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

func buildLinkConfigDeletePatch(managedInterfaces []string, existingByIf map[string]*netcfg.LinkConfigV1Alpha1) ([]byte, error) {
	if len(managedInterfaces) == 0 {
		return []byte(""), nil
	}

	var out bytes.Buffer
	first := true
	for _, iface := range managedInterfaces {
		if _, ok := existingByIf[iface]; !ok {
			continue
		}

		doc := linkConfigDeletePatchDoc{
			Kind:       "LinkConfig",
			APIVersion: "v1alpha1",
			Name:       iface,
			Patch:      "delete",
		}

		b, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if !first {
			out.WriteString("---\n")
		}
		out.Write(b)
		first = false
	}

	return out.Bytes(), nil
}

func linkConfigsFromMachineConfig(machineConfig []byte) (map[string]*netcfg.LinkConfigV1Alpha1, error) {
	cfg, err := configloader.NewFromBytes(machineConfig)
	if err != nil {
		return nil, fmt.Errorf("parse machine config: %w", err)
	}

	out := map[string]*netcfg.LinkConfigV1Alpha1{}
	for _, doc := range cfg.Documents() {
		lc, ok := doc.(*netcfg.LinkConfigV1Alpha1)
		if !ok {
			continue
		}

		iface := strings.TrimSpace(lc.MetaName)
		if iface == "" {
			continue
		}

		out[iface] = lc.DeepCopy()
	}

	return out, nil
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
