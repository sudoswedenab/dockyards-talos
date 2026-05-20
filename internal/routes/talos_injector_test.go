package routes

import (
	"strings"
	"testing"
)

func TestBuildLinkConfigPatch(t *testing.T) {
	patch, err := buildLinkConfigPatch([]Route{
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
