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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	linkconfigv1alpha3 "github.com/sudoswedenab/dockyards-talos/api/v1alpha3"
	talosroutes "github.com/sudoswedenab/dockyards-talos/internal/routes"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	handler "sigs.k8s.io/controller-runtime/pkg/handler"
)

const (
	defaultLinkConfigName        = "default"
	machineLinkConfigAnnKey      = "dockyards.io/link-config"
	machineLinkConfigStateAnnKey = "dockyards.io/link-config-state"
)

// +kubebuilder:rbac:groups=talos.dockyards.io,resources=linkconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
type LinkConfigReconciler struct {
	client.Client
	Injector talosroutes.Injector
}

type machineLinkConfigStateAnnotation struct {
	LinkConfigName       string              `json:"linkConfigName"`
	LinkConfigGeneration int64               `json:"linkConfigGeneration"`
	StaticRoutes         []talosroutes.Route `json:"staticRoutes,omitempty"`
	DefaultRoute         *talosroutes.Route  `json:"defaultRoute,omitempty"`
}

func (r *LinkConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)

	var machine clusterv1.Machine
	if err := r.Get(ctx, req.NamespacedName, &machine); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if machine.GetDeletionTimestamp() != nil {
		return ctrl.Result{}, nil
	}

	linkConfigName := defaultLinkConfigName
	if v, ok := machine.Annotations[machineLinkConfigAnnKey]; ok && v != "" {
		linkConfigName = v
	}

	lastAppliedState, err := extractLinkConfigStateFromMachine(&machine)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("decode machine annotation %q for %s: %w", machineLinkConfigStateAnnKey, machine.Name, err)
	}

	var lc linkconfigv1alpha3.LinkConfig
	if err := r.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: linkConfigName}, &lc); err != nil {
		if apierrors.IsNotFound(err) {
			managedInterfaces := managedInterfacesFromState(lastAppliedState)
			if lastAppliedState == nil || lastAppliedState.LinkConfigName != linkConfigName || len(managedInterfaces) == 0 {
				logger.Info("linkconfig not found for machine", "machine", machine.Name, "linkConfig", linkConfigName)

				return ctrl.Result{}, nil
			}

			node, err := extractNodeFromMachine(&machine)
			if err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}

			talosConfig, err := extractTalosConfigFromClusterSecret(ctx, r.Client, &machine)
			if err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}

			if err := r.Injector.EnsureRoutes(ctx, node, talosConfig, nil, nil, managedInterfaces); err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("ensure route cleanup for machine %s: %w", machine.Name, err)
			}

			annotationUpdated, err := r.persistReconciledLinkConfigState(ctx, &machine, linkConfigName, 0, nil, nil)
			if err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("persist route cleanup annotation for machine %s: %w", machine.Name, err)
			}

			logger.Info("machine routes cleaned after missing linkconfig", "machine", machine.Name, "linkConfig", linkConfigName, "managedInterfaces", len(managedInterfaces), "annotationUpdated", annotationUpdated)

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	staticRoutes, defaultRoute, err := extractDesiredRoutes(&lc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("extract desired routes: %w", err)
	}
	node, err := extractNodeFromMachine(&machine)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	talosConfig, err := extractTalosConfigFromClusterSecret(ctx, r.Client, &machine)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	needsGateway := false
	for i := range staticRoutes {
		if strings.TrimSpace(staticRoutes[i].Gateway) == "" {
			needsGateway = true

			break
		}
	}
	if !needsGateway && defaultRoute != nil && strings.TrimSpace(defaultRoute.Gateway) == "" {
		needsGateway = true
	}

	if needsGateway {
		derivedGateway, err := deriveGatewayFromVMPod(ctx, r.Client, &machine)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		for idx := range staticRoutes {
			if strings.TrimSpace(staticRoutes[idx].Gateway) == "" {
				staticRoutes[idx].Gateway = derivedGateway
			}
		}
		if defaultRoute != nil && strings.TrimSpace(defaultRoute.Gateway) == "" {
			defaultRoute.Gateway = derivedGateway
		}
	}

	managedInterfaces := mergeManagedInterfaces(lastAppliedState, staticRoutes, defaultRoute)

	if err := r.Injector.EnsureRoutes(ctx, node, talosConfig, staticRoutes, defaultRoute, managedInterfaces); err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("ensure routes for machine %s: %w", machine.Name, err)
	}

	annotationUpdated, err := r.persistReconciledLinkConfigState(ctx, &machine, lc.Name, lc.Generation, staticRoutes, defaultRoute)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("persist reconciled routes annotation for machine %s: %w", machine.Name, err)
	}

	logger.Info("machine routes reconciled", "machine", machine.Name, "linkConfig", lc.Name, "staticRoutes", len(staticRoutes), "hasDefaultRoute", defaultRoute != nil, "annotationUpdated", annotationUpdated)

	return ctrl.Result{}, nil
}

func (r *LinkConfigReconciler) persistReconciledLinkConfigState(ctx context.Context, machine *clusterv1.Machine, linkConfigName string, linkConfigGeneration int64, staticRoutes []talosroutes.Route, defaultRoute *talosroutes.Route) (bool, error) {
	payload := machineLinkConfigStateAnnotation{
		LinkConfigName:       linkConfigName,
		LinkConfigGeneration: linkConfigGeneration,
		StaticRoutes:         staticRoutes,
		DefaultRoute:         defaultRoute,
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal linkconfig annotation payload: %w", err)
	}

	oldValue := ""
	if machine.Annotations != nil {
		oldValue = machine.Annotations[machineLinkConfigStateAnnKey]
	}
	newValue := string(b)
	if oldValue == newValue {
		return false, nil
	}

	base := machine.DeepCopy()
	if machine.Annotations == nil {
		machine.Annotations = map[string]string{}
	}
	machine.Annotations[machineLinkConfigStateAnnKey] = newValue

	if err := r.Patch(ctx, machine, client.MergeFrom(base)); err != nil {
		return false, err
	}

	return true, nil
}

func extractLinkConfigStateFromMachine(machine *clusterv1.Machine) (*machineLinkConfigStateAnnotation, error) {
	if machine.Annotations == nil {
		return nil, nil
	}

	raw := strings.TrimSpace(machine.Annotations[machineLinkConfigStateAnnKey])
	if raw == "" {
		return nil, nil
	}

	var out machineLinkConfigStateAnnotation
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func mergeManagedInterfaces(lastAppliedState *machineLinkConfigStateAnnotation, staticRoutes []talosroutes.Route, defaultRoute *talosroutes.Route) []string {
	set := map[string]struct{}{}

	for _, route := range staticRoutes {
		iface := strings.TrimSpace(route.Interface)
		if iface == "" {
			continue
		}
		set[iface] = struct{}{}
	}
	if defaultRoute != nil {
		iface := strings.TrimSpace(defaultRoute.Interface)
		if iface != "" {
			set[iface] = struct{}{}
		}
	}

	for _, iface := range managedInterfacesFromState(lastAppliedState) {
		set[iface] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for iface := range set {
		out = append(out, iface)
	}
	sort.Strings(out)

	return out
}

func managedInterfacesFromState(state *machineLinkConfigStateAnnotation) []string {
	if state == nil {
		return nil
	}

	set := map[string]struct{}{}
	for _, route := range state.StaticRoutes {
		iface := strings.TrimSpace(route.Interface)
		if iface == "" {
			continue
		}
		set[iface] = struct{}{}
	}
	if state.DefaultRoute != nil {
		iface := strings.TrimSpace(state.DefaultRoute.Interface)
		if iface != "" {
			set[iface] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for iface := range set {
		out = append(out, iface)
	}
	sort.Strings(out)

	return out
}

func (r *LinkConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1.Machine{}).
		Watches(
			&linkconfigv1alpha3.LinkConfig{},
			handler.EnqueueRequestsFromMapFunc(r.linkConfigToMachines),
		).
		Complete(r)
}

func (r *LinkConfigReconciler) linkConfigToMachines(ctx context.Context, obj client.Object) []ctrl.Request {
	lc, ok := obj.(*linkconfigv1alpha3.LinkConfig)
	if !ok {
		return nil
	}

	var machineList clusterv1.MachineList
	if err := r.List(ctx, &machineList, client.InNamespace(lc.Namespace)); err != nil {
		return nil
	}

	reqs := make([]ctrl.Request, 0, len(machineList.Items))
	for i := range machineList.Items {
		machine := &machineList.Items[i]
		linkConfigName := defaultLinkConfigName
		if v, ok := machine.Annotations[machineLinkConfigAnnKey]; ok && v != "" {
			linkConfigName = v
		}

		if linkConfigName != lc.Name {
			continue
		}

		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(machine)})
	}

	return reqs
}

func extractDesiredRoutes(obj *linkconfigv1alpha3.LinkConfig) ([]talosroutes.Route, *talosroutes.Route, error) {
	staticRoutes := make([]talosroutes.Route, 0, len(obj.Spec.StaticRoutes))
	for i, route := range obj.Spec.StaticRoutes {
		if route.Network == "" {
			return nil, nil, fmt.Errorf("spec.staticRoutes[%d].network must be non-empty string", i)
		}
		if strings.TrimSpace(route.Interface) == "" {
			return nil, nil, fmt.Errorf("spec.staticRoutes[%d].interface must be non-empty string", i)
		}

		staticRoutes = append(staticRoutes, talosroutes.Route{
			Network:   route.Network,
			Gateway:   route.Gateway,
			Metric:    route.Metric,
			Interface: route.Interface,
		})
	}

	var defaultRoute *talosroutes.Route
	if obj.Spec.DefaultRoute != nil {
		if strings.TrimSpace(obj.Spec.DefaultRoute.Interface) == "" {
			return nil, nil, fmt.Errorf("spec.defaultRoute.interface must be non-empty string")
		}

		defaultRoute = &talosroutes.Route{
			Gateway:   obj.Spec.DefaultRoute.Gateway,
			Metric:    obj.Spec.DefaultRoute.Metric,
			Interface: obj.Spec.DefaultRoute.Interface,
		}
	}

	return staticRoutes, defaultRoute, nil
}

func extractNodeFromMachine(machine *clusterv1.Machine) (talosroutes.Node, error) {
	for _, a := range machine.Status.Addresses {
		if a.Type == clusterv1.MachineInternalIP || a.Type == clusterv1.MachineExternalIP {
			if a.Address != "" {
				return talosroutes.Node{
					Address:    a.Address,
					Port:       50000,
					MachineKey: fmt.Sprintf("%s/%s/%s", machine.Spec.ClusterName, machine.Namespace, machine.Name),
				}, nil
			}
		}
	}

	return talosroutes.Node{}, fmt.Errorf("machine %s has no usable IP address yet", machine.Name)
}

func extractTalosConfigFromClusterSecret(ctx context.Context, c client.Client, machine *clusterv1.Machine) ([]byte, error) {
	if machine.Spec.ClusterName == "" {
		return nil, fmt.Errorf("machine %s has empty spec.clusterName", machine.Name)
	}

	secretName := machine.Spec.ClusterName + "-talosconfig"
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: secretName}, &secret); err != nil {
		return nil, fmt.Errorf("get talosconfig secret %s/%s: %w", machine.Namespace, secretName, err)
	}

	data := secret.Data["talosconfig"]
	if len(data) == 0 {
		return nil, fmt.Errorf("talosconfig secret %s/%s missing key talosconfig", machine.Namespace, secretName)
	}

	return data, nil
}

const ovnPodNetworksAnnotationKey = "k8s.ovn.org/pod-networks"

const capkKubevirtMachineNameLabelKey = "capk.cluster.x-k8s.io/kubevirt-machine-name"

type ovnPodNetwork struct {
	IPAddress   string   `json:"ip_address"`
	IPAddresses []string `json:"ip_addresses"`
	GatewayIP   string   `json:"gateway_ip"`
	GatewayIPs  []string `json:"gateway_ips"`
}

func deriveGatewayFromVMPod(ctx context.Context, c client.Client, machine *clusterv1.Machine) (string, error) {
	machineIP, err := machineInternalIP(machine)
	if err != nil {
		return "", err
	}

	pod, err := findVMPodForMachine(ctx, c, machine)
	if err != nil {
		return "", err
	}

	raw := pod.Annotations[ovnPodNetworksAnnotationKey]
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("pod %s/%s missing annotation %q", pod.Namespace, pod.Name, ovnPodNetworksAnnotationKey)
	}

	gw, err := selectGatewayFromOVNPodNetworks(raw, machineIP)
	if err != nil {
		return "", fmt.Errorf("derive gateway from pod %s/%s annotation %q: %w", pod.Namespace, pod.Name, ovnPodNetworksAnnotationKey, err)
	}

	return gw, nil
}

func machineInternalIP(machine *clusterv1.Machine) (netip.Addr, error) {
	for _, a := range machine.Status.Addresses {
		if a.Type != clusterv1.MachineInternalIP {
			continue
		}
		ip, err := netip.ParseAddr(a.Address)
		if err != nil {
			continue
		}

		return ip, nil
	}

	return netip.Addr{}, fmt.Errorf("machine %s has no parseable internal IP address yet", machine.Name)
}

func findVMPodForMachine(ctx context.Context, c client.Client, machine *clusterv1.Machine) (*corev1.Pod, error) {
	if machine.Spec.InfrastructureRef.Name != "" {
		var pods corev1.PodList
		if err := c.List(ctx, &pods,
			client.InNamespace(machine.Namespace),
			client.MatchingLabels{capkKubevirtMachineNameLabelKey: machine.Spec.InfrastructureRef.Name},
		); err != nil {
			return nil, err
		}
		if len(pods.Items) > 0 {
			sort.SliceStable(pods.Items, func(i, j int) bool {
				a := strings.TrimSpace(pods.Items[i].Annotations[ovnPodNetworksAnnotationKey]) != ""
				b := strings.TrimSpace(pods.Items[j].Annotations[ovnPodNetworksAnnotationKey]) != ""

				return a && !b
			})

			return &pods.Items[0], nil
		}
	}

	if machine.Spec.InfrastructureRef.Name == "" {
		return nil, fmt.Errorf("no VM pod found for machine %s (machine.spec.infrastructureRef.name is empty)", machine.Name)
	}

	return nil, fmt.Errorf(
		"no VM pod found for machine %s (tried label %s=%s)",
		machine.Name,
		capkKubevirtMachineNameLabelKey,
		machine.Spec.InfrastructureRef.Name,
	)
}

func selectGatewayFromOVNPodNetworks(raw string, machineIP netip.Addr) (string, error) {
	var nets map[string]ovnPodNetwork
	if err := json.Unmarshal([]byte(raw), &nets); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	if len(nets) == 0 {
		return "", fmt.Errorf("annotation contains no networks")
	}

	for name, n := range nets {
		if ip, ok := parseFirstAddrFromPrefixString(n.IPAddress); ok && ip == machineIP {
			return gatewayFromOVNNetwork(name, n)
		}
		for _, p := range n.IPAddresses {
			if ip, ok := parseFirstAddrFromPrefixString(p); ok && ip == machineIP {
				return gatewayFromOVNNetwork(name, n)
			}
		}
	}

	keys := make([]string, 0, len(nets))
	for k := range nets {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return "", fmt.Errorf("no pod-networks entry matched machine internal IP %s (keys: %s)", machineIP.String(), strings.Join(keys, ", "))
}

func gatewayFromOVNNetwork(name string, n ovnPodNetwork) (string, error) {
	if strings.TrimSpace(n.GatewayIP) != "" {
		return strings.TrimSpace(n.GatewayIP), nil
	}
	for _, gw := range n.GatewayIPs {
		if strings.TrimSpace(gw) != "" {
			return strings.TrimSpace(gw), nil
		}
	}

	return "", fmt.Errorf("matched network %q has no gateway_ip/gateway_ips", name)
}

func parseFirstAddrFromPrefixString(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), true
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip, true
	}

	return netip.Addr{}, false
}
