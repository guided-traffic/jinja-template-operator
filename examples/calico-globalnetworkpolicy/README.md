# Example: Calico GlobalNetworkPolicy from a JinjaTemplate

This example renders a [Calico](https://docs.tigera.io/calico/latest/reference/resources/globalnetworkpolicy) `GlobalNetworkPolicy` that allows egress to the IPs behind a DNS name (here: `hans-fischer.com`). The IPs are resolved by the operator's `dns` source and the policy is re-rendered whenever the record changes.

## 1. Create the tenant ServiceAccount and grant it RBAC

RawObject outputs are applied via **ServiceAccount impersonation**: the operator acts as the ServiceAccount named in `spec.output.serviceAccountName` (same namespace as the CR). Neither the operator's ClusterRole nor the Helm chart grants any permissions on the target kind — they go to a tenant ServiceAccount:

```sh
kubectl apply -f rbac.yaml
```

See [rbac.yaml](rbac.yaml): it creates the ServiceAccount `gnp-applier` in the `infra` namespace and grants it `get`/`create`/`patch`/`delete` on `globalnetworkpolicies`. The `watch`/`list` verbs are not required — raw outputs are not watched by the operator.

You can verify the grant with standard tooling:

```sh
kubectl auth can-i create globalnetworkpolicies.crd.projectcalico.org \
  --as=system:serviceaccount:infra:gnp-applier
```

## 2. Create the JinjaTemplate

```sh
kubectl apply -f jinjatemplate.yaml
```

See [jinjatemplate.yaml](jinjatemplate.yaml) — note `spec.output.serviceAccountName: gnp-applier`. The rendered result looks like:

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

- If the ServiceAccount is missing, the CR reports `Ready=False` with reason `ServiceAccountNotFound`; if it lacks RBAC on the kind, the reason is `OutputForbidden` (with a `kubectl auth can-i` remediation hint in the message). Both retry with backoff — granting the permission makes the CR turn green without further action.
- Deleting the ServiceAccount acts as a revocation: the operator checks its existence fail-closed before every apply/delete. Remember to delete the RoleBindings/ClusterRoleBindings too — RBAC bindings match the bare name string and would authorize a recreated ServiceAccount of the same name.
- `GlobalNetworkPolicy` is cluster-scoped, so the generated object cannot carry an OwnerReference to the namespaced CR. The operator instead uses a finalizer on the CR and deletes the policy itself (as `gnp-applier`) when the CR is deleted (unless `setOwnerReference: false`).
- The DNS source re-resolves based on the record's TTL (or `refreshIntervalSeconds`) and re-renders the policy when the IP set changes. `removalGracePeriodSeconds` keeps IPs that briefly disappear from responses in the policy to avoid flapping.
- Raw outputs are not watched: if someone deletes the policy manually, it is restored on the next reconcile (DNS refresh, source change or operator restart), not immediately.
