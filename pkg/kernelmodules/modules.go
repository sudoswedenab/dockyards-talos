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

// Package kernelmodules ensures kernel modules are configured and loaded on
// Talos 1.14+ nodes. Callers must serialize configuration writes per node across
// all writers; this package does not provide distributed locking.
package kernelmodules

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/state"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosclientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	configres "github.com/siderolabs/talos/pkg/machinery/resources/config"
	runtimeres "github.com/siderolabs/talos/pkg/machinery/resources/runtime"
)

var moduleNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]*$`)

// Loader connects directly to a node's Talos API on port 50000.
// Its zero value is usable. A Loader can be shared by concurrent calls provided
// Timeout is not modified; callers still need to coordinate writes to each node.
type Loader struct {
	// Timeout bounds the complete operation. Values <= 0 default to 90 seconds.
	Timeout time.Duration
}

// EnsureModules adds missing module configuration and waits for every requested
// module to become Live or Builtin. It preserves existing module parameters and
// unrelated configuration, applies with NO_REBOOT, and never installs extensions,
// changes the image, removes modules, or configures sysctls.
//
// address must be a node IP, and talosconfig must contain Talos API credentials.
// Module names may contain ASCII letters, digits, underscores and hyphens, but
// cannot begin with a hyphen. Duplicate names are ignored; an empty list is a
// no-op without contacting the node.
//
// The staged-configuration checks are not concurrency control: callers must
// serialize this entire operation with other configuration writers for the node.
func (l *Loader) EnsureModules(ctx context.Context, address string, talosconfig []byte, modules []string) error {
	names, err := validateModules(modules)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if net.ParseIP(address) == nil {
		return fmt.Errorf("invalid Talos node IP %q", address)
	}
	cfg, err := talosclientconfig.FromBytes(talosconfig)
	if err != nil {
		return fmt.Errorf("parse talosconfig: %w", err)
	}
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := talosclient.New(ctx, talosclient.WithConfig(cfg), talosclient.WithEndpoints(net.JoinHostPort(address, "50000")))
	if err != nil {
		return fmt.Errorf("create Talos client: %w", err)
	}
	defer c.Close()

	return ensureModules(ctx, c.COSI, func(ctx context.Context, request *machineapi.ApplyConfigurationRequest) error {
		_, err := c.ApplyConfiguration(ctx, request)

		return err
	}, names)
}

func validateModules(modules []string) ([]string, error) {
	names := make([]string, 0, len(modules))
	seen := make(map[string]bool, len(modules))
	for _, name := range modules {
		if !moduleNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid kernel module name %q", name)
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}

	return names, nil
}

func ensureModules(ctx context.Context, st state.CoreState, apply func(context.Context, *machineapi.ApplyConfigurationRequest) error, modules []string) error {
	res, err := st.Get(ctx, resource.NewMetadata(configres.NamespaceName, configres.MachineConfigType, configres.ActiveID, resource.VersionUndefined))
	if err != nil {
		return fmt.Errorf("read active Talos machine configuration: %w", err)
	}
	machineConfig, ok := res.(*configres.MachineConfig)
	if !ok {
		return fmt.Errorf("unexpected Talos machine configuration resource %T", res)
	}
	body, err := machineConfig.Provider().Bytes()
	if err != nil {
		return fmt.Errorf("encode Talos machine configuration: %w", err)
	}
	patched, changed, err := addMissingModules(body, modules)
	if err != nil {
		return err
	}
	if changed {
		if err := checkPersistentConfig(ctx, st, machineConfig, body); err != nil {
			return err
		}
		if err := apply(ctx, &machineapi.ApplyConfigurationRequest{
			Data: patched, Mode: machineapi.ApplyConfigurationRequest_NO_REBOOT,
		}); err != nil {
			return fmt.Errorf("apply kernel module configuration without reboot: %w", err)
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		missing, err := unloadedModules(ctx, st, modules)
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kernel modules not loaded (%s); verify the Talos image contains the requested modules: %w", strings.Join(missing, ", "), ctx.Err())
		case <-ticker.C:
		}
	}
}

// checkPersistentConfig prevents overwriting staged or temporary changes with
// the active configuration. It is a safety check, not concurrency control.
func checkPersistentConfig(ctx context.Context, st state.CoreState, active *configres.MachineConfig, activeBody []byte) error {
	persistent, err := st.Get(ctx, resource.NewMetadata(configres.NamespaceName, configres.MachineConfigType, configres.PersistentID, resource.VersionUndefined))
	switch {
	case state.IsNotFoundError(err):
		// Booting from STATE may publish only the initial active configuration.
		// Later active versions without a persistent copy could be TRY changes.
		if active.Metadata().Version().Value() != 1 {
			return fmt.Errorf("persistent Talos configuration is absent and active configuration has changed; resolve staged/temporary changes before loading kernel modules")
		}

		return nil
	case err != nil:
		return fmt.Errorf("read persistent Talos machine configuration: %w", err)
	}

	persistentConfig, ok := persistent.(*configres.MachineConfig)
	if !ok {
		return fmt.Errorf("unexpected persistent Talos configuration resource %T", persistent)
	}
	persistentBody, err := persistentConfig.Provider().Bytes()
	if err != nil {
		return fmt.Errorf("encode persistent Talos configuration: %w", err)
	}
	if !bytes.Equal(activeBody, persistentBody) {
		return fmt.Errorf("active and persistent Talos configurations differ; resolve staged/temporary changes before loading kernel modules")
	}

	return nil
}

func addMissingModules(body []byte, modules []string) ([]byte, bool, error) {
	cfg, err := configloader.NewFromBytes(body)
	if err != nil {
		return nil, false, fmt.Errorf("parse Talos machine configuration: %w", err)
	}
	existing := make(map[string]bool)
	for _, module := range cfg.KernelModuleConfigs() {
		existing[module.Name()] = true
	}
	var patch bytes.Buffer
	for _, name := range modules {
		if existing[name] {
			continue
		}
		if patch.Len() > 0 {
			patch.WriteString("---\n")
		}
		fmt.Fprintf(&patch, "apiVersion: v1alpha1\nkind: KernelModuleConfig\nname: %s\n", name)
		existing[name] = true
	}
	if patch.Len() == 0 {
		return body, false, nil
	}
	p, err := configpatcher.LoadPatch(patch.Bytes())
	if err != nil {
		return nil, false, fmt.Errorf("load kernel module patch: %w", err)
	}
	result, err := configpatcher.Apply(configpatcher.WithBytes(body), []configpatcher.Patch{p})
	if err != nil {
		return nil, false, fmt.Errorf("merge kernel module patch: %w", err)
	}
	patched, err := result.Bytes()
	if err != nil {
		return nil, false, fmt.Errorf("encode patched Talos configuration: %w", err)
	}

	return patched, true, nil
}

func unloadedModules(ctx context.Context, st state.CoreState, modules []string) ([]string, error) {
	var missing []string
	for _, name := range modules {
		res, err := st.Get(ctx, resource.NewMetadata(runtimeres.NamespaceName, runtimeres.KernelModuleStatusType, name, resource.VersionUndefined))
		if state.IsNotFoundError(err) {
			missing = append(missing, name)

			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Talos module status %s: %w", name, err)
		}
		status, ok := res.(*runtimeres.KernelModuleStatus)
		if !ok {
			return nil, fmt.Errorf("unexpected Talos module status resource %T", res)
		}
		if status.TypedSpec().State != runtimeres.KernelModuleStateLive && status.TypedSpec().State != runtimeres.KernelModuleStateBuiltin {
			missing = append(missing, name)
		}
	}

	return missing, nil
}
