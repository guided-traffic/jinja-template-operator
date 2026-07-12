# Jinja Template Operator — Project Specification

## Project Overview

The **Jinja Template Operator** is a Kubernetes Operator written in **Go 1.25.7** that generates **ConfigMaps** or **Secrets** from Jinja-like templates. It uses [Gonja](https://github.com/guided-traffic/gonja) as its template engine and leverages `controller-runtime` for the operator framework.

The operator watches `JinjaTemplate` Custom Resources, resolves variable sources from existing ConfigMaps and Secrets, renders the Jinja template, and creates or updates the target output resource. It automatically re-renders when source data changes.

## Technical Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.26.0 |
| Template Engine | [Gonja](https://github.com/guided-traffic/gonja) (Jinja-like syntax for Go) |
| Operator Framework | controller-runtime (`sigs.k8s.io/controller-runtime`) |
| Go Module | `github.com/guided-traffic/jinja-template-operator` |
| Repository | `github.com/guided-traffic/jinja-template-operator` |
| Container Image | `guidedtraffic/jinja-template-operator` (Docker Hub) |
| Helm Chart | `jinja-template-operator` (under `deploy/helm/jinja-template-operator/`) |
| CI/CD | GitHub Actions with semantic-release |
| Linting | golangci-lint |
| Testing | Unit, Integration (envtest), E2E (Kind) |

## Custom Resource Definition

- **API Group:** `jto.gtrfc.com`
- **API Version:** `v1`
- **Kind:** `JinjaTemplate`
- **Full API:** `jto.gtrfc.com/v1`
- **Scope:** Namespaced (the CR itself is namespaced; the operator is cluster-scoped and watches all namespaces)

## CRD Spec Design

### Sources (`spec.sources`)

Each source provides variables for the Jinja template context. Rules:

- **Multiple sources** can be defined per CR.
- Each source has a **unique `name`** that becomes the variable name in the template.
- Each source references **either a ConfigMap or a Secret** (never both).
- Two reference modes per source:
  - **Direct reference** (`name` + `key`): resolves to a single string value.
  - **Label selector** (`labelSelector`): resolves to a **list of objects**, each with `name` (string) and `data` (map[string]string).
- Sources are **same-namespace only** — they must exist in the same namespace as the CR.

### Template (`spec.template` / `spec.templateFrom`)

- **Inline template** (`spec.template`): a string containing the Jinja template directly in the CR. This is the default.
- **External template** (`spec.templateFrom.configMapRef`): references a ConfigMap by `name` and `key` to load the template from.
- Exactly one of `template` or `templateFrom` must be provided.

### Output (`spec.output`)

- `spec.output.kind` (required): `ConfigMap`, `Secret` or `RawObject`.
- `spec.output.name` (optional): name of the generated resource. Defaults to the CR's own name if omitted. Must not be set for `RawObject`.
- `spec.output.key` (optional): the data key in the output ConfigMap or Secret where the rendered content is stored. Defaults to `"content"` if omitted. Must not be set for `RawObject`.
- `spec.output.serviceAccountName` (required for `RawObject`, forbidden otherwise): ServiceAccount in the CR's own namespace whose identity the operator impersonates for apply/delete of the raw output.

### Raw Object Output (`spec.output.kind: RawObject`)

- The rendered template must be a **single** YAML document forming a complete Kubernetes manifest (`apiVersion`, `kind`, `metadata.name`).
- **ServiceAccount impersonation:** the operator applies and deletes the object as `system:serviceaccount:<cr-namespace>:<spec.output.serviceAccountName>`. Authorization is standard Kubernetes RBAC granted to that ServiceAccount (`get`/`create`/`patch` for apply, `delete` for cleanup/finalization); auditable via `kubectl auth can-i … --as=system:serviceaccount:<ns>:<sa>`. There is no operator-side allowlist.
- **Fail-closed ServiceAccount check** before every apply/delete (uncached Get as the operator): a deleted ServiceAccount acts as a revocation. Missing SA ⇒ `Ready=False`/`ServiceAccountNotFound`; RBAC denial ⇒ `OutputForbidden` with remediation hint. Both return errors for backoff-requeue (SAs and RBAC are not watched).
- `status.lastOutput.serviceAccountName` records the creator identity; cleanup after target/ServiceAccount changes and finalization run under that identity.
- Namespaced kinds are written to the CR's own namespace only (cross-namespace targets are rejected) and use an OwnerReference for garbage collection.
- Cluster-scoped kinds cannot carry a namespaced OwnerReference; the operator uses the finalizer `jto.gtrfc.com/raw-output-cleanup` on the CR and deletes the object itself on CR deletion (unless owner-reference semantics are disabled). If the ServiceAccount/RBAC is gone, the CR stays Terminating (restore the SA, re-grant RBAC, or remove the finalizer manually).
- Objects are applied via server-side apply (field manager `jinja-template-operator`). Raw outputs are not watched for external changes.
- RBAC for the target kinds is **not** part of the Helm chart; it is granted explicitly to the tenant ServiceAccount (see `examples/calico-globalnetworkpolicy/`).
- Optional admission guard (`operator.rawObjects.authorCheck.enabled`, default `false`, K8s ≥ 1.30): ValidatingAdmissionPolicy requiring the CR author to hold `impersonate` on the referenced ServiceAccount (confused-deputy mitigation).

### Owner Reference (`spec.setOwnerReference`)

- Boolean field, optional.
- Controls whether the generated ConfigMap/Secret has an OwnerReference pointing to the JinjaTemplate CR.
- If `true`: generated resource is garbage-collected when the CR is deleted.
- If `false`: generated resource survives CR deletion.
- If omitted: falls back to the **global default** configured via the Helm chart values (`operator.defaultOwnerReference`, default: `true`).

## Example CR

```yaml
apiVersion: jto.gtrfc.com/v1
kind: JinjaTemplate
metadata:
  name: app-config
  namespace: my-app
spec:
  setOwnerReference: true

  sources:
    # Direct ConfigMap reference → single value
    - name: db_host
      configMap:
        name: database-config
        key: host

    # Direct Secret reference → single value
    - name: db_password
      secret:
        name: db-credentials
        key: password

    # Label selector on ConfigMaps → list of objects
    - name: endpoints
      configMap:
        labelSelector:
          matchLabels:
            type: endpoint

  template: |
    DATABASE_HOST={{ db_host }}
    DATABASE_PASSWORD={{ db_password }}
    {% for ep in endpoints %}
    # {{ ep.name }}
    {% for key, value in ep.data.items() %}
    {{ key }}={{ value }}
    {% endfor %}
    {% endfor %}

  output:
    kind: ConfigMap
    # name defaults to "app-config" (same as CR name)
    # key defaults to "content" if omitted
    key: app.env
```

## Reconciliation Behavior

- The operator **watches all namespaces** (cluster-scoped deployment).
- It reconciles on:
  - Changes to `JinjaTemplate` CRs.
  - Changes to any ConfigMap or Secret referenced by a source (direct or via label selector).
  - Creation/deletion of ConfigMaps/Secrets that match a label selector.
- On successful render: creates or updates the output ConfigMap/Secret.
- On failure: sets `Ready=False` condition with error message AND emits a Kubernetes Event.

## Status & Error Handling

The operator reports status via **Conditions** on the CR and **Kubernetes Events**:

| Condition | Status | Meaning |
|-----------|--------|---------|
| `Ready` | `True` | Template rendered successfully, output is up-to-date |
| `Ready` | `False` | Rendering failed (missing source, syntax error, etc.) |

Errors visible via `kubectl describe jinjatemplate <name>`.

## Helm Chart Configuration

The Helm chart is located at `deploy/helm/jinja-template-operator/` and deploys:

- CRD for `JinjaTemplate`
- Operator Deployment (single replica, cluster-scoped)
- ServiceAccount, ClusterRole, ClusterRoleBinding
- Optional: metrics Service, health probes

Key Helm values:

| Value | Description | Default |
|-------|-------------|---------|
| `operator.defaultOwnerReference` | Global default for OwnerReference on generated resources | `true` |
| `operator.rawObjects.authorCheck.enabled` | Optional VAP guard: CR authors need `impersonate` on the referenced ServiceAccount (K8s ≥ 1.30) | `false` |
| `image.repository` | Container image repository | `guidedtraffic/jinja-template-operator` |
| `image.tag` | Container image tag | `latest` |

## Project Structure (Target)

```
cmd/
  main.go                          # Entrypoint
api/
  v1/
    jinjatemplate_types.go         # CRD type definitions
    groupversion_info.go           # API group registration
    zz_generated.deepcopy.go       # Generated deep copy methods
internal/
  controller/
    jinjatemplate_controller.go    # Main reconciler logic
    jinjatemplate_controller_test.go
  sources/
    resolver.go                    # Source resolution (direct + label selector)
    resolver_test.go
  template/
    renderer.go                    # Gonja template rendering
    renderer_test.go
  config/
    config.go                      # Operator configuration (global defaults)
config/
  crd/
    bases/                         # Generated CRD YAML
  rbac/                            # RBAC manifests
  manager/                         # Manager deployment manifests
deploy/
  helm/
    jinja-template-operator/
      Chart.yaml
      values.yaml
      templates/
        deployment.yaml
        serviceaccount.yaml
        clusterrole.yaml
        clusterrolebinding.yaml
        crd.yaml
        _helpers.tpl
test/
  integration/                     # envtest-based integration tests
  e2e/                             # Kind cluster E2E tests
    helm-values.yaml
```
