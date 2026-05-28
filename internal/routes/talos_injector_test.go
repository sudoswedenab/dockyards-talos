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
	"strings"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	netcfg "github.com/siderolabs/talos/pkg/machinery/config/types/network"
)

func TestBuildLinkConfigPatch(t *testing.T) {
	patch, err := buildLinkConfigPatch([]string{"eth1"}, []Route{
		{Network: "100.66.0.0/16", Gateway: "100.66.3.1", Metric: 100, Interface: "eth1"},
		{Network: "100.96.0.0/12", Gateway: "100.66.3.1", Metric: 0, Interface: "eth1"},
		{Network: "", Gateway: "100.66.3.1", Metric: 0, Interface: "eth1"},
	}, map[string]*netcfg.LinkConfigV1Alpha1{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patch) == 0 {
		t.Fatalf("expected non-empty patch")
	}
}

func TestBuildLinkConfigPatch_IncludesEmptyRoutesForManagedInterface(t *testing.T) {
	patch, err := buildLinkConfigPatch([]string{"eth1"}, nil, map[string]*netcfg.LinkConfigV1Alpha1{
		"eth1": netcfg.NewLinkConfigV1Alpha1("eth1"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(patch), "name: eth1") {
		t.Fatalf("expected patch to target eth1, got:\n%s", string(patch))
	}
	if !strings.Contains(string(patch), "routes: []") {
		t.Fatalf("expected patch to clear routes with empty list, got:\n%s", string(patch))
	}
}

func TestMissingRoutesFromMachineConfig_SkipsExisting(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth1
routes:
  - destination: 100.66.0.0/16
    gateway: 100.66.3.1
    metric: 100
  - gateway: 100.66.3.1
    metric: 100
`) + "\n")

	missing, err := missingRoutesFromMachineConfig(base, []Route{
		{Network: "100.66.0.0/16", Gateway: "100.66.3.1", Metric: 100, Interface: "eth1"},
		{Network: "", Gateway: "100.66.3.1", Metric: 100, Interface: "eth1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected no missing routes, got %d", len(missing))
	}
}

func TestStrategicMergePatch_AppendsRoutesByDefault(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth0
routes:
  - destination: 83.255.255.26/32
    gateway: 100.66.11.1
`) + "\n")

	patchBytes := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth0
routes:
  - destination: 10.17.66.34/32
    gateway: 100.66.11.1
`) + "\n")

	patch, err := configpatcher.LoadPatch(patchBytes)
	if err != nil {
		t.Fatalf("load patch: %v", err)
	}

	patchedCfg, err := configpatcher.Apply(configpatcher.WithBytes(base), []configpatcher.Patch{patch})
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}

	out, err := patchedCfg.Bytes()
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}

	s := string(out)
	if !strings.Contains(s, "83.255.255.26/32") || !strings.Contains(s, "10.17.66.34/32") {
		t.Fatalf("expected both old and new routes in patched config, got:\n%s", s)
	}
}

func TestStrategicMergePatch_DocumentDeleteDirective(t *testing.T) {
	base := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth0
routes:
  - destination: 83.255.255.26/32
    gateway: 100.66.11.1
`) + "\n")

	patchBytes := []byte(strings.TrimSpace(`
apiVersion: v1alpha1
kind: LinkConfig
name: eth0
$patch: delete
`) + "\n")

	patch, err := configpatcher.LoadPatch(patchBytes)
	if err != nil {
		t.Fatalf("load patch: %v", err)
	}

	patchedCfg, err := configpatcher.Apply(configpatcher.WithBytes(base), []configpatcher.Patch{patch})
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}

	out, err := patchedCfg.Bytes()
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}

	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected linkconfig doc to be removed, got:\n%s", string(out))
	}
}
