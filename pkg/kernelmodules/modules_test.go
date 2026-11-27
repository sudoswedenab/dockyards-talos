// Copyright 2026 Sudo Sweden AB
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

package kernelmodules

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/cosi-project/runtime/pkg/state/impl/inmem"
	"github.com/cosi-project/runtime/pkg/state/impl/namespaced"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	configres "github.com/siderolabs/talos/pkg/machinery/resources/config"
	runtimeres "github.com/siderolabs/talos/pkg/machinery/resources/runtime"
)

var testModuleSets = map[string][]string{
	"network": {"vrf", "br_netfilter"},
	"nvidia":  {"nvidia", "nvidia_uvm", "nvidia_modeset", "nvidia_drm"},
}

type persistentReadFailureState struct {
	state.CoreState
}

func (s persistentReadFailureState) Get(ctx context.Context, pointer resource.Pointer, opts ...state.GetOption) (resource.Resource, error) {
	if pointer.Namespace() == configres.NamespaceName && pointer.Type() == configres.MachineConfigType && pointer.ID() == configres.PersistentID {
		return nil, context.DeadlineExceeded
	}
	return s.CoreState.Get(ctx, pointer, opts...)
}

func TestAddMissingModules(t *testing.T) {
	for set, modules := range testModuleSets {
		t.Run(set, func(t *testing.T) {
			for name, body := range map[string]string{
				"no modules": "version: v1alpha1\nmachine:\n  type: worker\n  nodeLabels:\n    example.com/keep: yes\n",
				"existing documents": "version: v1alpha1\nmachine:\n  type: worker\n---\napiVersion: v1alpha1\nkind: KernelModuleConfig\nname: " +
					modules[0] + "\nparameters:\n  - keep=1\n---\napiVersion: v1alpha1\nkind: KernelModuleConfig\nname: dummy\n",
				"legacy modules": "version: v1alpha1\nmachine:\n  type: worker\n  kernel:\n    modules:\n      - name: " +
					modules[0] + "\n        parameters:\n          - keep=1\n      - name: dummy\n",
			} {
				t.Run(name, func(t *testing.T) {
					patched, changed, err := addMissingModules([]byte(body), modules)
					if err != nil || !changed {
						t.Fatalf("patch: changed=%v, error=%v", changed, err)
					}
					cfg, err := configloader.NewFromBytes(patched)
					if err != nil {
						t.Fatal(err)
					}
					counts := map[string]int{}
					for _, module := range cfg.KernelModuleConfigs() {
						counts[module.Name()]++
						if module.Name() == modules[0] && name != "no modules" && !reflect.DeepEqual(module.Parameters(), []string{"keep=1"}) {
							t.Fatalf("existing parameters overwritten: %v", module.Parameters())
						}
					}
					for _, module := range modules {
						if counts[module] != 1 {
							t.Errorf("expected one config for %s, got %d", module, counts[module])
						}
					}
					if name != "no modules" && counts["dummy"] != 1 {
						t.Fatal("unrelated module configuration was lost")
					}
					if name == "no modules" && cfg.RawV1Alpha1().MachineConfig.MachineNodeLabels["example.com/keep"] != "yes" {
						t.Fatal("unrelated machine configuration was lost")
					}
					again, changed, err := addMissingModules(patched, modules)
					if err != nil || changed || !bytes.Equal(again, patched) {
						t.Fatalf("patch was not idempotent: changed=%v, error=%v", changed, err)
					}
				})
			}
		})
	}
	if _, _, err := addMissingModules([]byte("not valid: [yaml"), []string{"vrf"}); err == nil {
		t.Fatal("expected invalid machine configuration to be rejected")
	}
}

func TestAddMissingModulesPreservesOtherDocuments(t *testing.T) {
	body := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n---\napiVersion: v1alpha1\nkind: HostnameConfig\nhostname: keep-me\n")
	patched, changed, err := addMissingModules(body, []string{"vrf", "vrf", "br_netfilter"})
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v, error=%v", changed, err)
	}
	if !bytes.Contains(patched, []byte("kind: HostnameConfig")) || !bytes.Contains(patched, []byte("hostname: keep-me")) {
		t.Fatalf("unrelated document lost:\n%s", patched)
	}
	if bytes.Count(patched, []byte("name: vrf\n")) != 1 {
		t.Fatalf("duplicate module configuration:\n%s", patched)
	}
	unchanged, changed, err := addMissingModules(body, nil)
	if err != nil || changed || !bytes.Equal(unchanged, body) {
		t.Fatalf("empty list changed configuration: changed=%v, error=%v", changed, err)
	}
}

func TestUnloadedModules(t *testing.T) {
	ctx := t.Context()
	modules := []string{"vrf", "br_netfilter", "nvidia", "nvidia_uvm"}
	st := inmem.NewState(runtimeres.NamespaceName)
	missing, err := unloadedModules(ctx, st, modules)
	if err != nil || !reflect.DeepEqual(missing, modules) {
		t.Fatalf("expected all modules missing: %v, error=%v", missing, err)
	}
	for i, name := range modules {
		module := runtimeres.NewKernelModuleStatus(runtimeres.NamespaceName, name)
		module.TypedSpec().State = runtimeres.KernelModuleStateLive
		if i == 0 {
			module.TypedSpec().State = runtimeres.KernelModuleStateBuiltin
		}
		if i == 3 {
			module.TypedSpec().State = runtimeres.KernelModuleStateLoading
		}
		if err := st.Create(ctx, module); err != nil {
			t.Fatal(err)
		}
	}
	missing, err = unloadedModules(ctx, st, modules)
	if err != nil || !reflect.DeepEqual(missing, []string{"nvidia_uvm"}) {
		t.Fatalf("expected only loading module to be missing: %v, error=%v", missing, err)
	}
}

func TestLoaderRejectsInvalidInputs(t *testing.T) {
	l := &Loader{}
	if err := l.EnsureModules(t.Context(), "not an IP", nil, []string{"vrf"}); err == nil {
		t.Fatal("accepted invalid node IP")
	}
	if err := l.EnsureModules(t.Context(), "10.0.0.1", []byte("invalid: [yaml"), []string{"vrf"}); err == nil {
		t.Fatal("accepted invalid Talos credentials")
	}
	for _, name := range []string{"", " vrf", "vrf ", "-vrf", "../vrf", "vrf.ko", "vrf\n---\nkind: MachineConfig", "vrf: value"} {
		t.Run(name, func(t *testing.T) {
			if err := l.EnsureModules(t.Context(), "10.0.0.1", nil, []string{name}); err == nil || !strings.Contains(err.Error(), "invalid kernel module name") {
				t.Fatalf("invalid name not rejected before credential parsing: %v", err)
			}
		})
	}
}

func TestValidateModules(t *testing.T) {
	input := []string{"vrf", "br_netfilter", "vrf", "some-module", "8021q"}
	want := []string{"vrf", "br_netfilter", "some-module", "8021q"}
	got, err := validateModules(input)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("validation: got=%v, error=%v", got, err)
	}
	if !reflect.DeepEqual(input, []string{"vrf", "br_netfilter", "vrf", "some-module", "8021q"}) {
		t.Fatal("modified caller's slice")
	}
}

func TestLoaderEmptyModules(t *testing.T) {
	// No credentials or valid IP are needed because no connection should be made.
	l := &Loader{}
	if err := l.EnsureModules(t.Context(), "", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureModules(t *testing.T) {
	for set, modules := range testModuleSets {
		t.Run(set, func(t *testing.T) {
			testEnsureModules(t, modules)
		})
	}
}

func testEnsureModules(t *testing.T, modules []string) {
	t.Helper()
	const base = "version: v1alpha1\nmachine:\n  type: worker\n"
	complete, _, err := addMissingModules([]byte(base), modules)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{
		"apply missing modules",
		"booted from disk",
		"already configured",
		"staged changes",
		"temporary changes without persistent config",
		"persistent read fails",
		"modules not loaded",
		"apply fails",
	} {
		t.Run(scenario, func(t *testing.T) {
			st := namespaced.NewState(func(namespace resource.Namespace) state.CoreState {
				return inmem.NewState(namespace)
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			body := []byte(base)
			if scenario == "already configured" || scenario == "modules not loaded" {
				body = complete
			}
			cfg, err := configloader.NewFromBytes(body)
			if err != nil {
				t.Fatal(err)
			}
			active := configres.NewMachineConfig(cfg)
			if err := st.Create(ctx, active); err != nil {
				t.Fatal(err)
			}
			if scenario == "temporary changes without persistent config" {
				current, err := st.Get(ctx, active.Metadata())
				if err != nil {
					t.Fatal(err)
				}
				temporary, err := configloader.NewFromBytes([]byte(base + "  nodeLabels:\n    example.com/temporary: change\n"))
				if err != nil {
					t.Fatal(err)
				}
				updated := configres.NewMachineConfig(temporary)
				updated.Metadata().SetVersion(current.Metadata().Version())
				if err := st.Update(ctx, updated); err != nil {
					t.Fatal(err)
				}
			}
			persistent := cfg
			if scenario == "staged changes" {
				persistent, err = configloader.NewFromBytes([]byte(base + "  nodeLabels:\n    example.com/staged: change\n"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "booted from disk" && scenario != "temporary changes without persistent config" {
				if err := st.Create(ctx, configres.NewMachineConfigWithID(persistent, configres.PersistentID)); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "modules not loaded" {
				for _, name := range modules {
					module := runtimeres.NewKernelModuleStatus(runtimeres.NamespaceName, name)
					module.TypedSpec().State = runtimeres.KernelModuleStateLive
					if err := st.Create(ctx, module); err != nil {
						t.Fatal(err)
					}
				}
			}
			applied := false
			var moduleState state.CoreState = st
			if scenario == "persistent read fails" {
				moduleState = persistentReadFailureState{CoreState: st}
			}
			err = ensureModules(ctx, moduleState, func(_ context.Context, request *machineapi.ApplyConfigurationRequest) error {
				applied = true
				if request.Mode != machineapi.ApplyConfigurationRequest_NO_REBOOT || request.DryRun {
					t.Fatalf("unsafe application mode: %+v", request)
				}
				if _, changed, err := addMissingModules(request.Data, modules); err != nil || changed {
					t.Fatalf("applied config missing modules: changed=%v, error=%v", changed, err)
				}
				if scenario == "apply fails" {
					return context.DeadlineExceeded
				}
				return nil
			}, modules)
			switch scenario {
			case "apply missing modules", "booted from disk":
				if err != nil || !applied {
					t.Fatalf("did not apply missing modules: applied=%v, error=%v", applied, err)
				}
			case "already configured":
				if err != nil || applied {
					t.Fatalf("reapplied existing configuration: applied=%v, error=%v", applied, err)
				}
			case "staged changes", "temporary changes without persistent config":
				if err == nil || !strings.Contains(err.Error(), "staged/temporary changes") || applied {
					t.Fatalf("staged changes were not protected: applied=%v, error=%v", applied, err)
				}
			case "persistent read fails":
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "read persistent Talos machine configuration") || applied {
					t.Fatalf("persistent read error was not preserved: applied=%v, error=%v", applied, err)
				}
			case "modules not loaded":
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "verify the Talos image") || applied {
					t.Fatalf("missing module was not reported: applied=%v, error=%v", applied, err)
				}
			case "apply fails":
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "without reboot") || !applied {
					t.Fatalf("apply error not propagated: applied=%v, error=%v", applied, err)
				}
			}
		})
	}
}
