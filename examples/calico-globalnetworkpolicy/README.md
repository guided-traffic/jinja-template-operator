# Example: Calico GlobalNetworkPolicy from a JinjaTemplate

This example renders a [Calico](https://docs.tigera.io/calico/latest/reference/resources/globalnetworkpolicy) `GlobalNetworkPolicy` that allows egress to the IPs behind a DNS name (here: `hans-fischer.com`). The IPs are resolved by the operator's `dns` source and the policy is re-rendered whenever the record changes.

## 1. Allow the namespace to render the kind

RawObject outputs are denied by default. Bind the kind to the namespace of the `JinjaTemplate` CR via Helm values:

```yaml
# values.yaml
operator:
  rawObjects:
    allowlist:
      - namespaces:
          - infra
        kinds:
          - apiVersion: crd.projectcalico.org/v1
            kind: GlobalNetworkPolicy
```

## 2. Grant the operator RBAC for the kind

The operator's ClusterRole intentionally does **not** include permissions for arbitrary kinds. Grant them explicitly (adjust the ServiceAccount name/namespace to your release):

```sh
kubectl apply -f rbac.yaml
```

See [rbac.yaml](rbac.yaml). The `watch`/`list` verbs are not required — raw outputs are not watched by the operator.

## 3. Create the JinjaTemplate

```sh
kubectl apply -f jinjatemplate.yaml
```

See [jinjatemplate.yaml](jinjatemplate.yaml). The rendered result looks like:

```yaml
apiVersion: crd.projectcalico.org/v1
kind: GlobalNetworkPolicy
metadata:
  name: hans-fischer-com-access
spec:
  selector: has(gnp/hans-fischer-com-access)
  types:
    - Egress
  egress:
    - action: Allow
      protocol: TCP
      destination:
        nets:
          - 203.0.113.10/32
        ports:
          - 443
```

## Notes

- `GlobalNetworkPolicy` is cluster-scoped, so the generated object cannot carry an OwnerReference to the namespaced CR. The operator instead uses a finalizer on the CR and deletes the policy itself when the CR is deleted (unless `setOwnerReference: false`).
- The DNS source re-resolves based on the record's TTL (or `refreshIntervalSeconds`) and re-renders the policy when the IP set changes. `removalGracePeriodSeconds` keeps IPs that briefly disappear from responses in the policy to avoid flapping.
- Raw outputs are not watched: if someone deletes the policy manually, it is restored on the next reconcile (DNS refresh, source change or operator restart), not immediately.
