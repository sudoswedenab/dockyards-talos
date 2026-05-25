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
	"net/netip"
	"testing"
)

func TestSelectGatewayFromOVNPodNetworks_MatchesMachineIP(t *testing.T) {
	raw := `{"default":{"ip_addresses":["100.66.3.49/24"],"mac_address":"0a:58:64:42:03:31","gateway_ips":["100.66.3.1"],"routes":[{"dest":"100.66.0.0/16","nextHop":"100.66.3.1"}],"ip_address":"100.66.3.49/24","gateway_ip":"100.66.3.1","role":"primary"},"dockyards-fnjk4/lab-workload-2":{"ip_addresses":null,"mac_address":"76:8a:eb:94:f6:08","role":"secondary"}}`

	machineIP := netip.MustParseAddr("100.66.3.49")
	gw, err := selectGatewayFromOVNPodNetworks(raw, machineIP)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gw != "100.66.3.1" {
		t.Fatalf("expected gateway 100.66.3.1, got %q", gw)
	}
}
