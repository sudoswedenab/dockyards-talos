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
	"net/netip"
	"strings"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	netcfg "github.com/siderolabs/talos/pkg/machinery/config/types/network"
)

func TestReconcileManagedLinkRoutes_UpdatesRoutesInPlace(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth1
up: false
addresses:
  - address: 10.79.130.24/24
routes:
  - destination: 192.168.20.0/24
    gateway: 10.79.130.1
    metric: 100
`) + "\n")

	desiredByIf, err := desiredRouteConfigsByInterface([]string{"eth1"}, []Route{
		{Network: "100.66.0.0/16", Gateway: "10.79.130.1", Metric: 100, Interface: "eth1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, changed, err := reconcileManagedLinkRoutes(base, desiredByIf)
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if !changed {
		t.Fatal("expected config to change")
	}

	lc := mustGetLinkConfig(t, updated, "eth1")
	if len(lc.LinkAddresses) != 1 || lc.LinkAddresses[0].AddressAddress.String() != "10.79.130.24/24" {
		t.Fatalf("expected existing address to be preserved, got %#v", lc.LinkAddresses)
	}
	if len(lc.LinkRoutes) != 1 {
		t.Fatalf("expected one managed route, got %d", len(lc.LinkRoutes))
	}
	if lc.LinkRoutes[0].RouteDestination.String() != "100.66.0.0/16" || lc.LinkRoutes[0].RouteGateway.String() != "10.79.130.1" {
		t.Fatalf("unexpected managed route: %#v", lc.LinkRoutes[0])
	}
}


func TestReconcileManagedLinkRoutes_ClearsRoutesWithoutDeletingLinkConfig(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth1
addresses:
  - address: 10.79.130.24/24
routes:
  - destination: 192.168.20.0/24
    gateway: 10.79.130.1
`) + "\n")

	desiredByIf, err := desiredRouteConfigsByInterface([]string{"eth1"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, changed, err := reconcileManagedLinkRoutes(base, desiredByIf)
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if !changed {
		t.Fatal("expected route cleanup to change config")
	}

	lc := mustGetLinkConfig(t, updated, "eth1")
	if len(lc.LinkAddresses) != 1 || lc.LinkAddresses[0].AddressAddress.String() != "10.79.130.24/24" {
		t.Fatalf("expected link attributes to remain, got %#v", lc.LinkAddresses)
	}
	if len(lc.LinkRoutes) != 0 {
		t.Fatalf("expected managed routes to be cleared, got %d", len(lc.LinkRoutes))
	}
}

func TestReconcileManagedLinkRoutes_NoopWhenAlreadyDesired(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth1
routes:
  - destination: 100.66.0.0/16
    gateway: 10.79.130.1
    metric: 100
`) + "\n")

	desiredByIf, err := desiredRouteConfigsByInterface([]string{"eth1"}, []Route{
		{Network: "100.66.0.0/16", Gateway: "10.79.130.1", Metric: 100, Interface: "eth1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, changed, err := reconcileManagedLinkRoutes(base, desiredByIf)
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if changed {
		t.Fatal("expected no change when routes already match desired state")
	}
	if strings.TrimSpace(string(updated)) != strings.TrimSpace(string(base)) {
		t.Fatal("expected machine config bytes to remain unchanged on noop")
	}
}


func mustGetLinkConfig(t *testing.T, machineConfig []byte, iface string) *netcfg.LinkConfigV1Alpha1 {
	t.Helper()

	cfg, err := configloader.NewFromBytes(machineConfig)
	if err != nil {
		t.Fatalf("parse machine config: %v", err)
	}

	for _, doc := range cfg.Documents() {
		lc, ok := doc.(*netcfg.LinkConfigV1Alpha1)
		if !ok {
			continue
		}
		if strings.TrimSpace(lc.MetaName) != iface {
			continue
		}

		return lc
	}

	t.Fatalf("missing link config for interface %q", iface)

	return nil
}

func TestRouteConfigSignature_DefaultValues(t *testing.T) {
	sig := routeConfigSignature(netcfg.RouteConfig{
		RouteGateway: netcfg.Addr{Addr: netip.MustParseAddr("10.79.130.1")},
	})

	if sig.Gateway != "10.79.130.1" {
		t.Fatalf("unexpected gateway signature: %s", sig.Gateway)
	}
}
