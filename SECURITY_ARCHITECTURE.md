# Security Architecture

How the Jinja Template Operator handles trust, credentials and privileges — what the design defends against, and what it explicitly does not. Usage documentation is in [README.md](README.md), contributor documentation in [DEVELOPER.md](DEVELOPER.md).

> This is the security *design* document. For reporting a vulnerability see [Reporting a vulnerability](#reporting-a-vulnerability).

## Roles and trust boundaries

| Role | Identity | Can do | Trusted for |
|------|----------|--------|-------------|
| Cluster operator | Installs the Helm chart | Grants the operator its ClusterRole, decides `rbac.createAggregateClusterRoles` and the optional admission policy | Everything below — the install decision is the root of this trust chain |
| Operator controller | `system:serviceaccount:<release-ns>:<fullname>` | Read/write ConfigMaps and Secrets in **all** namespaces; `impersonate` **any** ServiceAccount cluster-wide; manage `JinjaTemplate` CRs and their status | Rendering only what a CR asks for, and never crossing the CR's namespace |
| CR author (tenant) | Whoever may `create`/`update` `jinjatemplates` in a namespace | Read any ConfigMap/Secret in that namespace *through* the operator; act as any ServiceAccount of that namespace for RawObject outputs | Nothing — treated as the primary adversary in this model |
| Target ServiceAccount | `system:serviceaccount:<cr-ns>:<spec.output.serviceAccountName>` | Whatever its own RBAC allows on the RawObject target kind | Being the intended authorization boundary for raw outputs |
| Consumer workload | Mounts the generated ConfigMap/Secret | Read the rendered result | Nothing |

```
                        ┌──────────────────────────────────────────────┐
   cluster operator ───▶│ Helm install: ClusterRole + impersonate rights│
                        └──────────────────────────────────────────────┘
                                            │ grants
   ═══════════════════════════ trust boundary ═══════════════════════════
                                            ▼
  namespace "my-app"                ┌───────────────┐
  ┌───────────────────────┐         │   Operator    │  cluster-scoped
  │ JinjaTemplate CR      │────────▶│  reconciler   │  reads ALL cm/secrets
  │  (author = tenant)    │         └───────┬───────┘  impersonates ANY sa
  │ ConfigMaps / Secrets  │◀────read────────┤
  └───────────────────────┘                 │
             ▲                              ├──▶ ConfigMap / Secret output
             │ same namespace only          │      (operator identity,
             │                              │       CR namespace only)
             └──────────────────────────────┤
                                            │  impersonation
   ═══════════════════ authorization boundary ═══════════════════════════
                                            ▼
                          system:serviceaccount:<cr-ns>:<sa>
                                            │  standard RBAC
                                            ▼
                              RawObject target (any kind,
                              cluster-scoped or CR namespace)

   outbound: DNS queries to the resolver named in the CR (or /etc/resolv.conf)
```

## Data and secret flow

1. **Ingest.** `Resolver` reads ConfigMaps and Secrets **only from the CR's own namespace** — through the operator's cluster-wide credentials, but the namespace is taken from the CR object, never from a spec field. There is no cross-namespace source reference in the API.
2. **Decode.** Secret values are decoded to plain `string`s and placed in the template context alongside ConfigMap values. From that point the template engine cannot tell them apart.
3. **Render.** Gonja compiles and executes the template in the operator's process memory. Rendered content is never logged; logs carry object names, kinds and counts.
4. **Write.**
   - `Secret` output: values written to `Data` (`type: Opaque`) — still Kubernetes-Secret-grade, i.e. base64 at rest unless etcd encryption is configured.
   - `ConfigMap` output: values written in clear text. **Rendering a Secret source into a ConfigMap output declassifies it** — the API deliberately allows this, and it is the CR author's decision.
   - `RawObject` output: the full manifest is applied under the tenant ServiceAccount's identity.
5. **Status.** `status` records object references, ServiceAccount names and resolved DNS records — **no rendered content and no source values**.
6. **Events.** Failure messages name objects and keys, and may quote template fragments returned by the engine. Anyone who can read Events in the namespace sees them; do not put secret material in template literals.

**Credential inventory:** the operator holds exactly one long-lived credential — its own projected ServiceAccount token. It never mints, stores or caches tokens for target ServiceAccounts: impersonation is a per-request HTTP header, so there is no exfiltrable credential artifact for the tenant identity. That is the reason impersonation was chosen over `TokenRequest` (see [PLAN.md](PLAN.md)).

## Isolation and tenancy

| Mechanism | Enforced where | Defends against |
|-----------|----------------|-----------------|
| Same-namespace sources | `Resolve` is called with `jt.Namespace`; no namespace field exists in `spec.sources` | Reading another tenant's ConfigMaps/Secrets |
| Same-namespace ServiceAccount | The impersonated user is built as `system:serviceaccount:<cr-namespace>:<name>` | Borrowing a privileged ServiceAccount from another namespace |
| Same-namespace outputs | ConfigMap/Secret outputs always use the CR namespace; namespaced RawObjects are rejected if they target a different one | Writing into a foreign namespace |
| Impersonation + RBAC | `applyRawObject` / `deleteRawOutput` act as the tenant SA; the API server authorizes | The operator's own broad rights being used for arbitrary kinds |
| Fail-closed SA existence check | Uncached `APIReader.Get` before every apply and delete | A deleted ServiceAccount continuing to work because RBAC bindings match name strings |
| Single-document manifest parsing | `singleYAMLDocument` + `UnmarshalStrict` | Smuggling a second object past review behind a `---` separator |
| `generateName` rejected | `parseRawObject` | Unbounded object creation with no stable identity to clean up |
| Optional VAP author check | `ValidatingAdmissionPolicy`, off by default | Confused deputy: a CR author using a ServiceAccount they may not impersonate |

**What this does *not* defend against:**

- **Namespace-level secret disclosure.** Anyone who may create a `JinjaTemplate` in a namespace can read *every* ConfigMap and Secret in it — a direct reference or a `matchLabels: {}` selector is enough — and can render the result into a ConfigMap. If a role grants `create jinjatemplates` without `get secrets`, that role is effectively a namespace-wide secret reader.
- **ServiceAccount borrowing inside the namespace.** Without the VAP guard, `create jinjatemplates` implies the right to act as *any* ServiceAccount that exists in that namespace, including one bound to a ClusterRole. Control which ServiceAccounts exist per namespace, or enable the guard.
- **Operator compromise.** `impersonate` on `serviceaccounts` is granted cluster-wide with no `resourceNames` restriction, and the operator can read every Secret in the cluster. A compromised operator process is equivalent to the union of all ServiceAccounts in the cluster. This is the standard trade-off of the impersonation pattern (Flux, kapp-controller, OLM v1 take the same one), but it is a real single point of failure.
- **Object hijacking through server-side apply.** Applies use `ForceOwnership`. If the tenant SA may patch a pre-existing object of the target kind and name, the rendered manifest takes over its fields — RawObject RBAC should be scoped with `resourceNames` where the kind supports it.
- **Cross-tenant resource exhaustion.** Template rendering has no timeout, output-size limit or per-CR budget, and all reconciles share one process. A pathological template (deep loops, huge expansion) burns CPU and memory for every tenant.
- **Egress control.** `spec.sources[].dns.nameserver` lets a CR author point the operator at an arbitrary `host:port`. The payload is a DNS query, but the destination is attacker-chosen — restrict operator egress with a NetworkPolicy if that matters.

## Privilege footprint

Operator ClusterRole ([clusterrole.yaml](deploy/helm/jinja-template-operator/templates/clusterrole.yaml), mirrored in [config/rbac/role.yaml](config/rbac/role.yaml)):

| Resource | Verbs | Why | Consequence if abused |
|----------|-------|-----|-----------------------|
| `jinjatemplates` | create, delete, get, list, patch, update, watch | Watch and manage CRs | Can create/delete CRs itself |
| `jinjatemplates/status`, `/finalizers` | get, patch, update | Conditions, DNS state, finalizer lifecycle | Could strand or free CRs |
| `configmaps` | create, delete, get, list, patch, update, watch (**all namespaces**) | Read sources and templates, write outputs | Read/write any ConfigMap in the cluster |
| `secrets` | create, delete, get, list, patch, update, watch (**all namespaces**) | Read Secret sources, write Secret outputs | Read/write **every Secret in the cluster** |
| `serviceaccounts` | get, **impersonate** (**cluster-wide, unrestricted**) | Fail-closed existence check + RawObject apply/delete as the tenant SA | Act as any ServiceAccount in the cluster |
| `events`, `events.k8s.io/events` | create, patch | User-visible diagnostics | Event spam |
| `coordination.k8s.io/leases` | full (only with `leaderElection.enabled`) | Leader election | Disrupt leadership |

Additionally granted by the chart when `rbac.createAggregateClusterRoles: true` (default): `admin` and `edit` get full CRUD on `jinjatemplates`, `view` gets read-only. Because CR creation implies namespace secret access and ServiceAccount borrowing, this hands every namespace editor those capabilities. `edit` can already read namespace Secrets directly, so the delta is mainly the RawObject impersonation path.

Runtime hardening (chart defaults): `runAsNonRoot`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, all capabilities dropped, `readOnlyRootFilesystem`, distroless non-root base image (`gcr.io/distroless/static-debian12:nonroot`), CPU/memory limits, single replica with leader election. No host mounts, no host network, no privileged ports.

## Validation

Defense happens in three layers:

1. **CRD schema** (API server): `output.kind` enum, `dns.recordType` enum with default `A`, minimums on `refreshIntervalSeconds` (≥ 1) and `removalGracePeriodSeconds` (≥ 0), required `output.kind`, `sources[].name`, `templateFrom` keys.
2. **Controller validation** (`validateSpec`, rejected as `InvalidSpec` without requeue): exactly one of `configMap`/`secret`/`dns` per source; non-empty source names and `dns.host`; exactly one of `template`/`templateFrom` unless `output.keys` is used; unique, non-empty output keys each with exactly one template source; `serviceAccountName` required for RawObject and forbidden otherwise; `name`/`key`/`keys` forbidden for RawObject.
3. **Output-time validation** (RawObject only): single YAML document, strict unmarshal, mandatory `apiVersion`/`kind`/`metadata.name`, no `generateName`, scope check via RESTMapper, namespace pinned to the CR's own.

**Not validated:** template content (any Gonja template is accepted — errors surface at render time as `RenderFailed`), rendered output size, and whether the resulting object is semantically sensible for its kind.

Optional admission layer — [rawobject-author-check-vap.yaml](deploy/helm/jinja-template-operator/templates/rawobject-author-check-vap.yaml), `operator.rawObjects.authorCheck.enabled`, K8s ≥ 1.30, `failurePolicy: Fail`, action `Deny`: on CREATE/UPDATE of a `JinjaTemplate` with `output.kind: RawObject`, the requesting user must hold `impersonate` on the referenced ServiceAccount. Off by default because with GitOps the "author" is the delivery controller's identity, which would then need that permission itself.

## Rotation and change propagation

| Change | How it propagates | Latency |
|--------|-------------------|---------|
| Source ConfigMap/Secret updated | Watch event → mapped to referencing CRs → re-render | Watch latency (seconds) |
| ConfigMap/Secret matching a selector created or deleted | Same path | Watch latency |
| Output ConfigMap/Secret deleted or edited externally | The CR's own output is watched → restored | Watch latency |
| Template ConfigMap updated | Watched via `templateFrom` (top-level and per output key) | Watch latency |
| DNS record changed | TTL-driven requeue, or `refreshIntervalSeconds`; removals honour `removalGracePeriodSeconds` | TTL / configured interval (TTL floored at 5 s) |
| **Tenant ServiceAccount deleted** | Fail-closed check makes the **next apply/delete** fail with `ServiceAccountNotFound` — this is the revocation path | Next reconcile |
| **RBAC granted or revoked** | **Not watched.** A revocation takes effect on the next apply attempt; a grant is picked up by the backoff requeue | Next reconcile / backoff |
| RawObject modified or deleted externally | Not watched — corrected on the next reconcile | Until the next source/DNS/CR event or restart |
| Operator RBAC or image changed | `helm upgrade` | Rollout |

Two consequences worth internalizing:

- **Revoking access requires deleting the ServiceAccount, not just its bindings** — but deleting bindings alone is *also* insufficient in reverse: RBAC bindings match the bare name string, so recreating a ServiceAccount with the same name restores its permissions. Remove both.
- **A failed reconcile leaves the previous output in place.** If a source key disappears, the CR goes `Ready=False` while the last successfully rendered ConfigMap/Secret keeps serving stale content. Alert on `Ready=False`; do not assume outputs are self-invalidating.

## Residual risks — hardening checklist

- [ ] **Treat `create jinjatemplates` as equivalent to namespace-wide Secret read.** Audit any custom Role that grants it, and review `rbac.createAggregateClusterRoles: true` against your tenancy model.
- [ ] **Enable `operator.rawObjects.authorCheck.enabled`** on clusters ≥ 1.30 where CRs are authored by humans; otherwise keep the set of ServiceAccounts per namespace minimal and reviewed.
- [ ] **Scope tenant RBAC narrowly** — target kind only, `resourceNames` where supported, and no `delete` unless finalization needs it. Never grant target-kind permissions to the operator's own ServiceAccount.
- [ ] **Restrict the unauthenticated metrics endpoint.** `--metrics-bind-address` is parsed in [cmd/main.go](cmd/main.go) but never passed to the manager, so controller-runtime's default applies: plain HTTP on `:8080`, no TLS, no authentication. Nothing exposes it beyond the pod IP today (the chart ships no Service), but any pod that can reach the operator pod can scrape it. Add a NetworkPolicy, and wire `Metrics: metricsserver.Options{...}` with `SecureServing` before exposing it via a Service.
- [ ] **Restrict operator egress** with a NetworkPolicy if arbitrary `dns.nameserver` targets are a concern.
- [ ] **Enable etcd encryption at rest** — Secret outputs are only as protected as the cluster's Secret storage.
- [ ] **Review templates that read Secret sources into ConfigMap outputs.** Nothing prevents that declassification; it is not detectable after the fact.
- [ ] **Alert on `Ready=False` and `DNSHealthy=False`.** Stale outputs are served silently; DNS lookups deliberately keep the last known records on failure.
- [ ] **Watch for stuck finalizers.** A CR owning a cluster-scoped RawObject stays `Terminating` if its ServiceAccount or RBAC is gone. Remedies: restore the SA, re-grant RBAC, or remove `jto.gtrfc.com/raw-output-cleanup` manually — the last one abandons the object.
- [ ] **Bound template cost.** There is no render timeout or output-size limit; a hostile or accidental template affects every tenant sharing the operator. Consider a dedicated operator instance for untrusted tenants.
- [ ] **Constrain RawObject SSA takeovers.** `ForceOwnership` means an apply can seize an existing object the SA may patch; prefer dedicated names and `resourceNames`-scoped grants.
- [ ] **Keep the RBAC mirror in sync.** `config/rbac/role.yaml` and the chart's ClusterRole are maintained separately; a permission added in one and forgotten in the other is a silent drift.

## Reporting a vulnerability

Report privately — do **not** open a public issue, PR or discussion for a suspected vulnerability.

- Use GitHub's private vulnerability reporting on [guided-traffic/jinja-template-operator](https://github.com/guided-traffic/jinja-template-operator) (*Security* → *Report a vulnerability*).
- Include affected version/image tag, cluster version, a minimal `JinjaTemplate` reproducing the issue, and the impact you observed.
- The repository currently ships no `SECURITY.md` with a coordinated-disclosure policy or response SLA; treat timelines as best effort until one is published.
