# Dockyards Talos

`dockyards-talos` is a controller-runtime based component for Talos-related control-plane tasks in Dockyards.

It currently bundles three main areas of functionality:

- `Release` reconciliation for Dockyards release metadata (`dockyards.io/v1alpha3`)
- `LinkConfig` reconciliation for Talos node routes (`talos.dockyards.io/v1alpha3`)
- optional Talos discovery gRPC service

## What This Component Runs

Primary manager binary:

- `cmd/dockyards-talos/main.go`
  - runs the controller manager
  - runs `DockyardsReleaseReconciler`
  - runs `LinkConfigReconciler`
  - can optionally enable webhook handling
  - can optionally host discovery service in-process (`--enable-discovery-service=true`)

Standalone discovery binary:

- `cmd/discovery-service/main.go`
  - runs only the discovery gRPC service

## LinkConfig Routes Reconciler

The merged routes functionality from `dockyards-talos-routes` is now part of this component.

It includes:

- API types for `talos.dockyards.io/v1alpha3` `LinkConfig` (`api/v1alpha3`)
- `LinkConfigReconciler` (`controllers/linkconfig_controller.go`)
- Talos injector implementation (`internal/routes`)
- CRD and RBAC manifests under `config/`

### Expected LinkConfig Shape

```yaml
apiVersion: talos.dockyards.io/v1alpha3
kind: LinkConfig
metadata:
  name: default
  namespace: org-a
spec:
  staticRoutes:
  - network: 192.168.20.0/24
    metric: 100
    interface: eth1
  - network: 172.16.2.0/24
    interface: eth1
  defaultRoute:
    interface: eth1
```

Notes:

- `spec.staticRoutes[].gateway` and `spec.defaultRoute.gateway` are optional.
- If gateway is omitted, the controller derives it from VM pod annotation `k8s.ovn.org/pod-networks` by matching against the `Machine` internal IP.
- `spec.staticRoutes[].interface` and `spec.defaultRoute.interface` are required.
- Static routes are applied first; default route is applied after static routes are verified.

Machine selection behavior:

- Default `LinkConfig` name is `default` in the machine namespace.
- A machine can override this with annotation:

```yaml
metadata:
  annotations:
    dockyards.io/link-config: custom-routes
```

### Talos Authentication and Route Application

For each reconciled machine, the controller:

- reads Talos credentials from secret `<clusterName>-talosconfig` in the machine namespace (key `talosconfig`)
- connects to Talos API on machine IP and port `50000`
- patches node machine config with Talos `LinkConfig` documents via `ApplyConfiguration`
- verifies desired routes via Talos `RouteSpecs.net.talos.dev`
- stores the last reconciled payload on `Machine.metadata.annotations["dockyards.io/link-config-state"]` as JSON (linkconfig name/generation, static routes, default route)

### Gateway Derivation Details

When gateway is not explicitly provided, the controller:

- finds KubeVirt launcher pod by label `capk.cluster.x-k8s.io/kubevirt-machine-name=<Machine.spec.infrastructureRef.name>`
- reads annotation `k8s.ovn.org/pod-networks`
- selects the network entry matching the machine internal IP
- uses `gateway_ip` (or first value in `gateway_ips`) as route gateway

## Release Reconciler

`DockyardsReleaseReconciler` watches `dockyards.io/v1alpha3` `Release` resources and maintains Flux image resources used for release discovery:

- creates/patches `ImageRepository`
- creates/patches `ImagePolicy`
- updates `Release.status` with discovered versions
- for Talos installer releases, computes latest installer URL using image factory host and optional annotations:
  - `talos.dockyards.io/platform-name`
  - `talos.dockyards.io/schematic-id`

## Webhook

Optional webhook (`webhooks/dockyardsnodepool_webhook.go`) validates NodePool memory requirements.

## Manifests

Main manifest directories:

- `config/base`
- `config/rbac`
- `config/crd`
- `config/webhook`

Top-level kustomization artifact generation is driven by `hack/kustomization.cue` and includes base, CRD, RBAC, and webhook resources.

## Development

```bash
go mod tidy
go test ./...
go build ./cmd/... ./controllers ./internal/... ./api/... ./webhooks
```
