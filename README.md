# Jinja Template Operator

[![Test and Release](https://github.com/guided-traffic/jinja-template-operator/actions/workflows/release.yml/badge.svg)](https://github.com/guided-traffic/jinja-template-operator/actions/workflows/release.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/guided-traffic/jinja-template-operator/main/.github/badges/coverage.json)](https://github.com/guided-traffic/jinja-template-operator)
[![Docker Hub](https://img.shields.io/docker/v/guidedtraffic/jinja-template-operator?label=Docker%20Hub&sort=semver)](https://hub.docker.com/r/guidedtraffic/jinja-template-operator)
[![License](https://img.shields.io/github/license/guided-traffic/jinja-template-operator)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/guided-traffic/gonja)](https://github.com/guided-traffic/gonja/blob/main/go.mod)

A Kubernetes Operator that generates **ConfigMaps** and **Secrets** using [Jinja-like templates](https://github.com/guided-traffic/gonja). Define your template variables from existing ConfigMaps or Secrets — either by direct reference or via label selectors — and let the operator render and manage the output automatically.

## Features

- 🎨 **Jinja-like Templating** — Powered by [Gonja](https://github.com/guided-traffic/gonja), supporting filters, loops, conditionals, and more
- 📦 **Flexible Sources** — Reference individual ConfigMap/Secret keys or select entire groups via label selectors
- 🔄 **Reactive Reconciliation** — Automatic re-rendering when source ConfigMaps/Secrets change or new matches appear
- 🏷️ **Dynamic Label Selectors** — Automatically discovers new ConfigMaps/Secrets matching your selectors
- 🔐 **ConfigMap or Secret Output** — Generate either a ConfigMap or Secret per template
- 📝 **Inline & External Templates** — Define templates directly in the CR or reference an external ConfigMap
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
| `spec.template` | `string` | No² | Inline Jinja template |
| `spec.templateFrom.configMapRef.name` | `string` | No² | ConfigMap containing the template |
| `spec.templateFrom.configMapRef.key` | `string` | No² | Key within the ConfigMap holding the template |
| `spec.output.kind` | `string` | Yes | `ConfigMap` or `Secret` |
| `spec.output.name` | `string` | No | Name of the generated resource (defaults to CR name) |
| `spec.output.key` | `string` | No | Data key in the output ConfigMap/Secret (defaults to `content`) |

> ¹ Each source must specify either `configMap` or `secret` (not both). Within each, use either `name`+`key` (direct reference) or `labelSelector` (list).
>
> ² Either `spec.template` (inline) or `spec.templateFrom` (external) must be provided.

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

## Status & Error Handling

The operator reports status via **Conditions** and **Kubernetes Events**:

| Condition | Status | Meaning |
|-----------|--------|---------|
| `Ready` | `True` | Template rendered successfully, output resource is up-to-date |
| `Ready` | `False` | Rendering failed (syntax error, missing source, etc.) |

Errors are also emitted as Kubernetes Events, visible via:
```bash
kubectl describe jinjatemplate <name>
```

## Development

### Prerequisites
- Go 1.25.7
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
