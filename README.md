# Jinja Template Operator

[![Test and Release](https://github.com/guided-traffic/jinja-template-operator/actions/workflows/release.yml/badge.svg)](https://github.com/guided-traffic/jinja-template-operator/actions/workflows/release.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/guided-traffic/jinja-template-operator/main/.github/badges/coverage.json)](https://github.com/guided-traffic/jinja-template-operator)
[![Docker Hub](https://img.shields.io/docker/v/guidedtraffic/jinja-template-operator?label=Docker%20Hub&sort=semver)](https://hub.docker.com/r/guidedtraffic/jinja-template-operator)
[![License](https://img.shields.io/github/license/guided-traffic/jinja-template-operator)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/guided-traffic/jinja-template-operator)](https://github.com/guided-traffic/jinja-template-operator/blob/main/go.mod)

A Kubernetes Operator that generates **ConfigMaps** and **Secrets** using [Jinja-like templates](https://github.com/guided-traffic/gonja). Define your template variables from existing ConfigMaps or Secrets — either by direct reference or via label selectors — and let the operator render and manage the output automatically.

## Features

- 🎨 **Jinja-like Templating** — Powered by [Gonja](https://github.com/guided-traffic/gonja), supporting filters, loops, conditionals, and more
- 📦 **Flexible Sources** — Reference individual ConfigMap/Secret keys or select entire groups via label selectors
- 🔄 **Reactive Reconciliation** — Automatic re-rendering when source ConfigMaps/Secrets change or new matches appear
- 🏷️ **Dynamic Label Selectors** — Automatically discovers new ConfigMaps/Secrets matching your selectors
- 🔐 **ConfigMap or Secret Output** — Generate either a ConfigMap or Secret per template
- 📝 **Inline & External Templates** — Define templates directly in the CR or reference an external ConfigMap
- 🗝️ **Multi-Key Output** — Emit multiple independently-rendered keys in a single output ConfigMap/Secret
- 🔗 **Configurable OwnerReference** — Control whether output resources are garbage-collected with the CR
- 🌐 **Cluster-Scoped** — A single operator instance watches all namespaces

## Installation

### Helm Chart

```bash
helm repo add jinja-template-operator https://guided-traffic.github.io/jinja-template-operator
helm repo update
helm install jinja-template-operator jinja-template-operator/jinja-template-operator \
  --namespace jinja-template-operator-system \
  --create-namespace
```

### Helm Values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `operator.defaultOwnerReference` | Global default for OwnerReference on generated resources | `true` |
| `image.repository` | Container image repository | `guidedtraffic/jinja-template-operator` |
| `image.tag` | Image tag (defaults to chart `appVersion`) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `replicaCount` | Number of operator replicas | `1` |
| `rbac.createAggregateClusterRoles` | Create aggregate ClusterRoles for admin/edit/view | `true` |
| `leaderElection.enabled` | Enable leader election for HA | `true` |
| `logLevel` | Operator log level | `info` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `128Mi` |
| `resources.requests.cpu` | CPU request | `10m` |
| `resources.requests.memory` | Memory request | `64Mi` |

## Usage

### Custom Resource: `JinjaTemplate`

**API Group:** `jto.gtrfc.com/v1`

### Example 1: Inline Template with Direct Sources

```yaml
apiVersion: jto.gtrfc.com/v1
kind: JinjaTemplate
metadata:
  name: app-config
  namespace: my-app
spec:
  setOwnerReference: true

  sources:
    - name: db
      configMap:
        name: database-config
        key: connection

    - name: credentials
      secret:
        name: db-credentials
        key: password

  template: |
    DATABASE_HOST={{ db }}
    DATABASE_PASSWORD={{ credentials }}
    DATABASE_URL=postgres://admin:{{ credentials }}@{{ db }}:5432/mydb

  output:
    kind: ConfigMap
    key: app.env
```

### Example 2: Label Selector with Loop

```yaml
apiVersion: jto.gtrfc.com/v1
kind: JinjaTemplate
metadata:
  name: aggregated-endpoints
  namespace: platform
spec:
  sources:
    - name: services
      configMap:
        labelSelector:
          matchLabels:
            app.kubernetes.io/part-of: platform
            type: endpoint

  template: |
    # Auto-generated endpoint list
    {% for svc in services %}
    # Source: {{ svc.name }}
    {% for key, value in svc.data.items() %}
    {{ key }}={{ value }}
    {% endfor %}
    {% endfor %}

  output:
    kind: ConfigMap
    name: all-endpoints
    key: endpoints.conf
```

### Example 3: External Template + Mixed Sources

```yaml
apiVersion: jto.gtrfc.com/v1
kind: JinjaTemplate
metadata:
  name: nginx-config
  namespace: webserver
spec:
  setOwnerReference: false

  sources:
    - name: upstream_servers
      secret:
        labelSelector:
          matchLabels:
            role: upstream

    - name: tls_cert
      secret:
        name: tls-certificate
        key: cert.pem

    - name: server_settings
      configMap:
        name: nginx-defaults
        key: settings

  templateFrom:
    configMapRef:
      name: nginx-template
      key: nginx.conf.j2

  output:
    kind: Secret
    name: nginx-rendered-config
    key: nginx.conf
```

### Example 4: Multi-Key Output

Emit multiple keys into a single `Secret` (or `ConfigMap`), each rendered
independently. Use this for credential bundles such as DB or object-storage
secrets where each entry needs its own key.

```yaml
apiVersion: jto.gtrfc.com/v1
kind: JinjaTemplate
metadata:
  name: my-db-credentials
  namespace: my-app
spec:
  sources:
    - name: db_password
      secret:
        name: my-db-user
        key: password

  output:
    kind: Secret
    name: my-db-credentials
    keys:
      - key: DB_HOST
        template: "db-cluster-rw.my-namespace.svc.cluster.local"
      - key: DB_PORT
        template: "5432"
      - key: DB_NAME
        template: "myapp"
      - key: DB_PASSWORD
        template: |                    # multi-line block scalars are supported
          {{ db_password }}
```

Notes:

- Each entry in `output.keys` requires a `key` and exactly one of `template`
  (inline) or `templateFrom.configMapRef` (external), mirroring the top-level
  fields.
- When `output.keys` is set, the top-level `spec.template` / `spec.templateFrom`
  and `spec.output.key` are ignored.
- Rendered values are trimmed of leading/trailing whitespace before being
  written.
- The output resource contains **exactly** the declared keys — any key
  previously written by the operator that is no longer in `output.keys` is
  removed on the next reconcile.
- Existing `JinjaTemplates` without `output.keys` keep working unchanged.

## Spec Reference

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.setOwnerReference` | `bool` | No | Override global default for OwnerReference on the generated resource |
| `spec.sources` | `[]Source` | Yes | List of variable sources for the template |
| `spec.sources[].name` | `string` | Yes | Unique variable name, used in the template |
| `spec.sources[].configMap.name` | `string` | No¹ | Name of a specific ConfigMap |
| `spec.sources[].configMap.key` | `string` | No¹ | Key within the ConfigMap |
| `spec.sources[].configMap.labelSelector` | `LabelSelector` | No¹ | Select multiple ConfigMaps by labels (returns a list) |
| `spec.sources[].secret.name` | `string` | No¹ | Name of a specific Secret |
| `spec.sources[].secret.key` | `string` | No¹ | Key within the Secret |
| `spec.sources[].secret.labelSelector` | `LabelSelector` | No¹ | Select multiple Secrets by labels (returns a list) |
| `spec.sources[].dns.host` | `string` | No¹ | DNS name to resolve (returns a sorted list of IP strings) |
| `spec.sources[].dns.recordType` | `string` | No | `A` (default), `AAAA` or `A+AAAA`. CNAME chains are followed (max 10 hops); the result contains only IPs |
| `spec.sources[].dns.refreshIntervalSeconds` | `int` | No | Re-resolve after a fixed interval. If omitted, the record TTL drives the refresh |
| `spec.sources[].dns.nameserver` | `string` | No | DNS server (`host` or `host:port`, port defaults to 53). Defaults to the system resolver |
| `spec.sources[].dns.removalGracePeriodSeconds` | `int` | No | Keep a record in the list this long after it disappears from lookup responses (default: remove immediately) |
| `spec.template` | `string` | No² | Inline Jinja template |
| `spec.templateFrom.configMapRef.name` | `string` | No² | ConfigMap containing the template |
| `spec.templateFrom.configMapRef.key` | `string` | No² | Key within the ConfigMap holding the template |
| `spec.output.kind` | `string` | Yes | `ConfigMap` or `Secret` |
| `spec.output.name` | `string` | No | Name of the generated resource (defaults to CR name) |
| `spec.output.key` | `string` | No | Data key in the output ConfigMap/Secret (defaults to `content`). Ignored when `output.keys` is set. |
| `spec.output.keys` | `[]OutputKey` | No³ | List of independently-rendered key/template pairs written into the output resource |
| `spec.output.keys[].key` | `string` | Yes | Data key in the output ConfigMap/Secret |
| `spec.output.keys[].template` | `string` | No⁴ | Inline Jinja template for this key's value (trimmed of surrounding whitespace) |
| `spec.output.keys[].templateFrom.configMapRef.name` | `string` | No⁴ | ConfigMap containing the template for this key |
| `spec.output.keys[].templateFrom.configMapRef.key` | `string` | No⁴ | Key within the ConfigMap holding the template for this key |

> ¹ Each source must specify exactly one of `configMap`, `secret` or `dns`. Within `configMap`/`secret`, use either `name`+`key` (direct reference) or `labelSelector` (list).
>
> ² Either `spec.template` (inline) or `spec.templateFrom` (external) must be provided — unless `spec.output.keys` is set, in which case both are ignored.
>
> ³ When `spec.output.keys` is set, the top-level `template`/`templateFrom` and `output.key` are ignored; the output resource contains exactly the declared keys.
>
> ⁴ Each entry in `output.keys` must provide exactly one of `template` (inline) or `templateFrom.configMapRef` (external).

## Template Context

### Direct Reference (`name` + `key`)
The value of the specified key is available directly under the source name:
```jinja
{{ my_source_name }}
```

### Label Selector
Results are available as a list of objects, each containing `name` and `data`:
```jinja
{% for item in my_source_name %}
  Name: {{ item.name }}
  {% for key, value in item.data.items() %}
    {{ key }}={{ value }}
  {% endfor %}
{% endfor %}
```

### DNS Source
The lookup result is always a sorted list of IP address strings:
```jinja
{% for ip in my_dns_source %}
  server {{ ip }};
{% endfor %}
```

```yaml
sources:
  - name: backend_ips
    dns:
      host: backend.example.com
      recordType: A            # A (default), AAAA or A+AAAA
      refreshIntervalSeconds: 60   # omit to follow the record TTL
      nameserver: 10.96.0.10       # omit to use the system resolver
      removalGracePeriodSeconds: 300
```

DNS source semantics:

- The result is always a **sorted list of IPs** — also for a single record. CNAME chains are resolved transparently (max 10 hops).
- The operator re-reconciles based on the record **TTL**, or after `refreshIntervalSeconds` if set.
- **`removalGracePeriodSeconds`**: an IP that disappears from lookup responses stays in the list for this period before being removed. New IPs appear immediately. NXDOMAIN counts as an empty (successful) response, so records age out through the grace period.
- **Lookup failures** (timeout, SERVFAIL): the last known records stay valid indefinitely, `Ready` remains `True`, and the `DNSHealthy` condition turns `False` (plus a Warning Event). Failed lookups do not age records. Only if the *first ever* lookup fails does `Ready` turn `False`.
- Resolved state is persisted in `status.dnsSources` and survives operator restarts.

## Status & Error Handling

The operator reports status via **Conditions** and **Kubernetes Events**:

| Condition | Status | Meaning |
|-----------|--------|---------|
| `Ready` | `True` | Template rendered successfully, output resource is up-to-date |
| `Ready` | `False` | Rendering failed (syntax error, missing source, etc.) |
| `DNSHealthy` | `True` | All DNS source lookups succeed (only present when DNS sources are configured) |
| `DNSHealthy` | `False` | At least one DNS lookup fails; the last known records are still in use |

Errors are also emitted as Kubernetes Events, visible via:
```bash
kubectl describe jinjatemplate <name>
```

## Development

### Prerequisites
- Go 1.26.0
- Docker
- Kind (for E2E tests)
- Helm

### Build & Test

```bash
# Build
make build

# Run locally
make run

# Unit tests
make test-unit

# Integration tests
make test-integration

# E2E tests (requires Kind cluster)
make e2e-local

# Linting
make lint

# Security scan
make gosec
```

### Docker

```bash
# Build image
make docker-build

# Push image
make docker-push
```

## License

This project is licensed under the Apache License 2.0 — see the [LICENSE](LICENSE) file for details.
