# Talos kernel modules

Independent Go module for configuring and loading kernel modules on Talos nodes:

```text
github.com/sudoswedenab/dockyards-talos/pkg/kernelmodules
```

Requires Go 1.27.3+ and targets Talos 1.14+. The module depends on Talos
machinery and COSI, not the Dockyards controller or Kubernetes.

## Usage

```go
import (
    "context"
    "time"

    "github.com/sudoswedenab/dockyards-talos/pkg/kernelmodules"
)

func ensureNetworkModules(ctx context.Context, nodeIP string, talosconfig []byte) error {
    loader := kernelmodules.Loader{Timeout: 90 * time.Second}
    return loader.EnsureModules(ctx, nodeIP, talosconfig, []string{"vrf", "br_netfilter"})
}
```

The zero-value loader uses a 90-second timeout. Empty module lists are a no-op;
duplicate names are ignored. Names must consist of ASCII letters, digits,
underscores or hyphens and must not start with a hyphen.

The caller supplies credentials, selects nodes, handles retries, and has network
access to each node's Talos API on port 50000. No Kubernetes readiness check is
performed.

## Safety and limitations

- Adds only missing `KernelModuleConfig` documents; preserves existing module
  parameters, legacy module configuration and unrelated documents.
- Refuses writes when active and persistent configurations differ. When Talos
  omits the persistent resource after booting from STATE, only active version 1
  is accepted for a write.
- Applies with `NO_REBOOT` and waits for each requested module's status to become
  `Live` or `Builtin`. Failure or timeout is returned to the caller.
- Does not install drivers/extensions, change images, unload modules, remove
  configuration, or set sysctls. The Talos image must already contain the modules.
- **Does not coordinate concurrent configuration writes.** It reads, merges and
  submits the full machine configuration. All configuration writers must
  serialize per-node changes externally, holding coordination
  across the entire call. Active/persistent checks do not prevent lost updates
  from concurrent writers. A shared per-node Kubernetes Lease is one option.

## Development and publishing

This directory has its own `go.mod` to avoid imposing Talos 1.14 dependencies on
the parent controller. Root-level `go test ./...` does not test this module.

Run from this directory:

```sh
go test ./...
go vet ./...
```

Tests use in-memory COSI state, not a live Talos or Kubernetes cluster.

The repository's release-please workflow manages this module independently of
the root controller. Its first release is configured as `0.1.0`, with tags such as
`pkg/kernelmodules/v0.1.0` (nested Go modules require the directory prefix).
The manifest's initial `0.0.0` entry means the module has not been released yet.
Release-please creates `pkg/kernelmodules/CHANGELOG.md` in its release PR.

Use a release-triggering conventional commit, for example
`feat(kernelmodules): add reusable Talos kernel module loader`, when merging the
initial implementation. Merge the resulting release PR to publish the GitHub
release and tag; no separate Go artifact upload is required. Changes confined
to this module do not trigger a root controller release or release-image build.

After the first release is published, consumers can run:

```sh
go get github.com/sudoswedenab/dockyards-talos/pkg/kernelmodules@v0.1.0
```

Consumers can also use a commit version once these changes are published.
