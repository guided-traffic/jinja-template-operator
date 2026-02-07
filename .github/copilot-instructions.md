# Jinja Template Operator — Project Specification

## Project Overview

The **Jinja Template Operator** is a Kubernetes Operator written in **Go 1.25.7** that generates **ConfigMaps** or **Secrets** from Jinja-like templates. It uses [Gonja](https://github.com/guided-traffic/gonja) as its template engine and leverages `controller-runtime` for the operator framework.

The operator watches `JinjaTemplate` Custom Resources, resolves variable sources from existing ConfigMaps and Secrets, renders the Jinja template, and creates or updates the target output resource. It automatically re-renders when source data changes.

## Technical Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.25.7 |
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

- `spec.output.kind` (required): either `ConfigMap` or `Secret`.
- `spec.output.name` (optional): name of the generated resource. Defaults to the CR's own name if omitted.

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

---

## Implementation Plan

### Phase 1: Project Foundation
- [x] Initialize Go module with correct path (`github.com/guided-traffic/jinja-template-operator`)
- [x] Set up directory structure (`cmd/`, `api/`, `internal/`, `config/`, `test/`)
- [x] Create `cmd/main.go` with operator manager bootstrap (controller-runtime)
- [x] Add Gonja dependency to `go.mod`

### Phase 2: CRD & API Types
- [x] Define `JinjaTemplate` types in `api/v1/jinjatemplate_types.go` (Spec, Status, Source, Output)
- [x] Create `api/v1/groupversion_info.go` for API group registration
- [x] Generate deep copy methods (`zz_generated.deepcopy.go`)
- [x] Generate CRD YAML manifests
- [x] Write unit tests for type validation

### Phase 3: Source Resolver
- [x] Implement direct ConfigMap reference resolver (name + key → string)
- [x] Implement direct Secret reference resolver (name + key → string)
- [x] Implement label selector ConfigMap resolver (labelSelector → list of objects)
- [x] Implement label selector Secret resolver (labelSelector → list of objects)
- [x] Build context map from resolved sources (name → value/list)
- [x] Write unit tests for all resolver paths
- [x] Handle error cases: missing source, missing key, permission denied

### Phase 4: Template Renderer
- [x] Implement inline template rendering via Gonja
- [x] Implement external template loading from ConfigMap reference
- [x] Validate template before rendering (syntax check)
- [x] Write unit tests for rendering (variables, loops, filters, error cases)

### Phase 5: Reconciler (Controller)
- [x] Implement `JinjaTemplateReconciler` with `Reconcile()` method
- [x] Wire source resolution → template rendering → output creation/update
- [x] Implement ConfigMap output creation with proper labels/annotations
- [x] Implement Secret output creation with proper labels/annotations
- [x] Implement OwnerReference logic (CR field → global default fallback)
- [x] Set Status conditions (`Ready=True/False`) with messages
- [x] Emit Kubernetes Events on success and failure
- [x] Implement output name defaulting (CR name if `output.name` is omitted)
- [x] Write unit tests for reconciler logic

### Phase 6: Watch & Trigger Setup
- [x] Set up watch on `JinjaTemplate` CRs
- [x] Set up watch on ConfigMaps with mapping to owning JinjaTemplates
- [x] Set up watch on Secrets with mapping to owning JinjaTemplates
- [x] Ensure label selector matches trigger reconciliation for new/deleted resources
- [ ] Write integration tests (envtest) for watch behavior

### Phase 7: Operator Configuration
- [x] Implement global config struct (defaultOwnerReference, etc.)
- [x] Pass global config via CLI flags / environment variables from Helm
- [x] Wire config into reconciler

### Phase 8: RBAC & Manifests
- [x] Define ClusterRole with permissions for ConfigMaps, Secrets, JinjaTemplates, Events
- [x] Create ClusterRoleBinding
- [x] Create ServiceAccount
- [x] Create Manager Deployment manifest

### Phase 9: Helm Chart
- [x] Create `Chart.yaml` with metadata
- [x] Create `values.yaml` with all configurable values
- [x] Create `_helpers.tpl` with common template helpers
- [x] Create `templates/crd.yaml` for the JinjaTemplate CRD
- [x] Create `templates/deployment.yaml` for the operator
- [x] Create `templates/serviceaccount.yaml`
- [x] Create `templates/clusterrole.yaml` and `templates/clusterrolebinding.yaml`
- [ ] Validate chart with `helm lint` and `helm template`

### Phase 10: Integration & E2E Tests
- [ ] Write integration tests using envtest (create CR → verify output ConfigMap/Secret)
- [ ] Write integration tests for source change → re-render
- [ ] Write integration tests for label selector dynamic matching
- [ ] Write integration tests for error handling (missing source, bad template)
- [x] Create E2E test helm-values.yaml
- [ ] Write E2E tests against Kind cluster
- [ ] Verify CI pipeline runs successfully

### Phase 11: Documentation & Cleanup
- [ ] Finalize README.md with accurate examples
- [ ] Clean up unused files from the previous project
- [ ] Verify all CI workflows pass
- [ ] Tag initial release
