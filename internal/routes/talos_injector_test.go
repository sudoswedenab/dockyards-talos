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
)

func TestBuildLinkConfigPatch(t *testing.T) {
	patch, err := buildLinkConfigPatch([]string{"eth1"}, []Route{
		{Network: "100.66.0.0/16", Gateway: "100.66.3.1", Metric: 100, Interface: "eth1"},
		{Network: "100.96.0.0/12", Gateway: "100.66.3.1", Metric: 0, Interface: "eth1"},
		{Network: "", Gateway: "100.66.3.1", Metric: 0, Interface: "eth1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patch) == 0 {
		t.Fatalf("expected non-empty patch")
	}
}

func TestBuildLinkConfigPatch_IncludesEmptyRoutesForManagedInterface(t *testing.T) {
	patch, err := buildLinkConfigPatch([]string{"eth1"}, nil)
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
