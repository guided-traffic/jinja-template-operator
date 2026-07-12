# Plan: RawObject-Autorisierung via ServiceAccount-Impersonation

Stand: 2026-07-12 · Branch `feat/raw-objects` · Status: **umgesetzt**

## Ziel

RawObject-Outputs werden nicht mehr über die Operator-eigene Namespace/GVK-Allowlist
autorisiert, sondern über einen im CR benannten ServiceAccount, dessen Identität der
Operator per **Impersonation** für Apply und Delete annimmt.

Effekt:
- Operator braucht keine globalen Rechte auf Ziel-Kinds mehr (kein Calico-RBAC am Operator-SA).
- Allowlist (Helm-Value → ConfigMap → File-Mount → Flag) entfällt komplett, inkl. Pflege.
- Autorisierung = Standard-K8s-RBAC am referenzierten SA; auditierbar mit Bordmitteln
  (`kubectl auth can-i --as=system:serviceaccount:<ns>:<sa> …`).

Entspricht dem Ökosystem-Muster: Flux (`spec.serviceAccountName`, Impersonation),
kapp-controller (SA Pflicht), OLM v1 (SA Pflicht), Sveltos.

## Beschlossene Entscheidungen

| # | Entscheidung | Begründung |
|---|--------------|------------|
| 1 | **Impersonation**, nicht TokenRequest | Kein exfiltrierbares Credential-Artefakt (Token ≥ 10 min Lebensdauer); Audit-Event enthält beide Identitäten (`user` = Operator-SA, `impersonatedUser` = Ziel-SA) |
| 2 | `spec.output.serviceAccountName` **Pflicht bei RawObject**, **verboten bei ConfigMap/Secret** (InvalidSpec) | Kein unsicherer Default (Lehre aus Flux' opt-in-Lockdown); ConfigMap/Secret-Verhalten bleibt unverändert, kein Migrations-Impact |
| 3 | SA **immer im Namespace des CR** (nur Name, kein Namespace-Feld; Operator baut `system:serviceaccount:<cr-ns>:<name>`) | Cross-Namespace-Borrowing strukturell ausgeschlossen |
| 4 | Operator-ClusterRole: `impersonate` auf `serviceaccounts` **unbeschränkt** (kein resourceNames-Pinning) + `get` auf `serviceaccounts` | Nutzerentscheidung: volle SA-Namensfreiheit pro Namespace; Same-Namespace-Regel wird im Code erzwungen |
| 5 | Allowlist **komplett löschen** (Code, Flag, Helm, Docs, Tests) | SA-RBAC ist die einzige Autorisierungsquelle; kein zweites bespoke-System |
| 6 | **VAP-Guard optional im Chart, default aus** (`operator.rawObjects.authorCheck.enabled`) | Schließt Confused-Deputy (CR-Autor muss selbst `impersonate` auf den SA dürfen); default aus, weil GitOps-Controller sonst selbst impersonate-Recht bräuchte; braucht K8s ≥ 1.30 (VAP GA, `authorizer`-CEL) |
| 7 | **Fail-closed SA-Existenz-Check** vor jedem Apply/Delete (Get als Operator) | Impersonation funktioniert sonst auch nach SA-Löschung weiter (RBAC bindet an Namens-String) — SA-Löschung soll wie Revocation wirken |
| 8 | `status.lastOutput.serviceAccountName` speichert Erzeuger-Identität | Cleanup nach SA-Wechsel/Kind-Wechsel läuft unter der Identität, die das Objekt angelegt hat |

## Was vom aktuellen Stand BLEIBT

- Rendered Template = einzelnes YAML-Dokument, komplettes Manifest (Parsing inkl. Multi-Doc-Erkennung)
- Namespaced Kinds: nur eigener Namespace, OwnerReference-GC
- Cluster-scoped Kinds: Finalizer `jto.gtrfc.com/raw-output-cleanup`, Operator löscht selbst
- Server-Side Apply (Field-Manager `jinja-template-operator`), Labels, Target-Change-Cleanup
- `output.name`/`key`/`keys` bei RawObject verboten

## Was ERSETZT wird

Allowlist-Autorisierung → SA-Impersonation. Konkret zu löschen:

- `internal/config/rawobject_allowlist.go` + `_test.go`
- `OperatorConfig.RawObjectAllowlist` (config.go)
- `cmd/main.go`: Flag `--raw-object-allowlist-file` + Loader
- Controller: Allowlist-Gate + Reason `RawObjectDenied`
- Helm: `templates/rawobject-allowlist-configmap.yaml`, Deployment-Mount/-Arg/-Checksum, Value `operator.rawObjects.allowlist`
- Doku-Abschnitte zur Allowlist (README, CLAUDE.md), zugehörige Unit-/Integrationstests

## Umsetzung

### 1. API (`api/v1/jinjatemplate_types.go` + CRD-YAML handgepflegt + `make sync-helm-crd`)

- `Output.ServiceAccountName string` — Kommentar: Pflicht bei RawObject, gleicher Namespace, Identität für Apply/Delete.
- `OutputRef.ServiceAccountName string` (optional) — Erzeuger-Identität.
- CRD-Validierung (CEL im YAML): `has(serviceAccountName) == (kind == 'RawObject')`;
  Pattern DNS-1123-Label (verhindert `ns/name`- oder `system:serviceaccount:`-Injection).
  `validateSpec` als Backstop für bestehende Objekte.

### 2. Controller

- Reconciler-Feld `RestConfig *rest.Config` (aus `mgr.GetConfig()`); Factory-Seam
  `rawClientFor(namespace, saName) (client.Client, error)`:
  `rest.CopyConfig` + `rest.ImpersonationConfig{UserName: "system:serviceaccount:<ns>:<sa>"}`
  + `client.New(cfg, client.Options{Scheme: r.Scheme, Mapper: r.RESTMapper()})`.
  Mapper/Discovery bleiben Operator-Identität. Client pro Reconcile, kein Cache (Transport wird von client-go gepoolt).
  Feld injizierbar für Unit-Tests (fake client kann Impersonation nicht simulieren).
- Ablauf RawObject-Reconcile:
  1. Parse + Namespace-Regeln (unverändert)
  2. **SA-Check**: Get SA als Operator → fehlt: `Ready=False`, Reason `ServiceAccountNotFound`,
     Warning-Event, **return error** (Backoff-Requeue — SAs werden nicht gewatcht)
  3. Finalizer wie bisher (cluster-scoped + Owner-Semantik)
  4. Cleanup altes Objekt bei Target-Änderung: **impersonierter Client mit `lastOutput.serviceAccountName`**
     (Fallback: aktueller Spec-SA, falls leer)
  5. Apply via impersoniertem Client. `IsForbidden` → Reason `OutputForbidden`, Message mit
     exakter Identität + `kubectl auth can-i create <resource>.<group> --as=system:serviceaccount:<ns>:<sa>`-
     Remediation, Warning-Event, **return error** (Backoff — RBAC-Grants triggern keinen Reconcile,
     Denies dürfen nicht terminal sein)
  6. `lastOutput` inkl. `serviceAccountName` setzen
- Finalizer-Pfad (`finalizeRawOutput`): Delete via impersoniertem Client (SA aus `lastOutput`).
  Forbidden/SA-fehlt → Warning-Event `FinalizeForbidden` mit Remediation + Retry (CR bleibt Terminating).
  README dokumentiert Auswege: RBAC re-granten / SA wiederherstellen / manuell Finalizer entfernen.
- Switch RawObject → ConfigMap/Secret: Delete des alten Raw-Objekts via `lastOutput`-SA.
- Benötigte SA-Verben dokumentieren: `get`, `create`, `patch` (SSA) + `delete` (Cleanup/Finalizer) aufs Ziel-Kind.

### 3. `cmd/main.go`

- Allowlist-Flag/Loader raus; `RestConfig: mgr.GetConfig()` an Reconciler.

### 4. Helm

- ClusterRole ergänzen:
  ```yaml
  - apiGroups: [""]
    resources: ["serviceaccounts"]
    verbs: ["get", "impersonate"]
  ```
- Allowlist-ConfigMap/Mount/Arg/Checksum entfernen.
- Neu: `templates/rawobject-author-check-vap.yaml` (ValidatingAdmissionPolicy + Binding),
  gated durch `operator.rawObjects.authorCheck.enabled` (default `false`):
  CEL `authorizer.group('').resource('serviceaccounts').namespace(object.metadata.namespace).name(object.spec.output.serviceAccountName).check('impersonate').allowed()`,
  matchCondition nur für CRs mit `spec.output.kind == 'RawObject'`, failurePolicy `Fail`.
- Values: `operator.rawObjects.authorCheck.enabled: false` (+ Kommentar K8s ≥ 1.30, GitOps-Caveat).

### 5. Example (`examples/calico-globalnetworkpolicy/`)

- `rbac.yaml`: SA `gnp-applier` im Namespace `infra` + ClusterRole (globalnetworkpolicies:
  get/create/patch/delete) + ClusterRoleBinding **an den Tenant-SA** (nicht mehr an den Operator-SA).
- `jinjatemplate.yaml`: `spec.output.serviceAccountName: gnp-applier`.
- README: Walkthrough anpassen (kein Operator-RBAC-Schritt mehr, kein Allowlist-Schritt).

### 6. Tests

- Unit (fake client): Factory-Seam injizieren; Fälle — Feld fehlt bei RawObject / Feld bei
  ConfigMap gesetzt (InvalidSpec), SA fehlt (`ServiceAccountNotFound` + error), Erfolg,
  Cleanup nutzt `lastOutput`-SA, Finalizer-Pfad.
- Integration (envtest, RBAC aktiv, Test-User = system:masters → darf impersonaten):
  - Erfolg: SA + Role/ClusterRole + Binding anlegen → Objekt entsteht
  - Forbidden: SA ohne RBAC → `Ready=False` `OutputForbidden`; Grant nachschieben → wird grün (Backoff-Requeue)
  - Finalizer: cluster-scoped Output wird beim CR-Delete via SA gelöscht
  - SA fehlt → `ServiceAccountNotFound`; SA anlegen → wird grün
  - Achtung Fixture: RBAC-Escalation-Prevention — Grants im Test als admin anlegen
- Bestehende Allowlist-Tests entfernen.

### 7. Doku

- README: Example 5 + Spec-Referenz + Helm-Values-Tabelle umstellen; Security-Abschnitt:
  - Restlücke: `create jinjatemplates` im Namespace ⇒ Nutzung jedes dort existierenden SA
    (Mitigation: VAP-Guard aktivieren oder SA-Bestand im Namespace kontrollieren)
  - SA-Löschung ist ohne Binding-Löschung keine vollständige Revocation (Operator prüft
    Existenz fail-closed, aber Bindings mit aufräumen!)
- CLAUDE.md: RawObject-Abschnitt ersetzen (Allowlist → SA-Impersonation).

## Risiken / bewusste Trade-offs

- `impersonate serviceaccounts` unbeschränkt: kompromittierter Operator kann jeden SA im
  Cluster werden (de-facto cluster-admin). Bewusst akzeptiert (Nutzerentscheidung,
  Ecosystem-Standard bei Flux/kapp/OLM). Härtungsoptionen für später: resourceNames-Pinning
  auf Konventionsnamen; ab K8s 1.36 Constrained Impersonation (`impersonate-on:serviceaccount:<verb>`).
- Stuck-Terminating bei gelöschtem RBAC/SA vor CR-Delete: designte Auswege dokumentiert (s.o.).
- Day-2-Discoverability: „wer darf was" ist jetzt RBAC-Join statt einer Datei —
  Punkt-Abfragen via `kubectl auth can-i --as=…`, Enumeration via rbac-lookup/who-can.

## Todos

- [x] **API**: `Output.ServiceAccountName` + `OutputRef.ServiceAccountName`, CRD-YAML (CEL-Validierung, Pattern), `make sync-helm-crd`
- [x] **Controller**: Impersonation-Factory (`rawClientFor`), fail-closed SA-Check, neue Reasons (`ServiceAccountNotFound`, `OutputForbidden`, `FinalizeForbidden`), Finalizer + Cleanup via `lastOutput`-SA, Backoff-Requeue bei Forbidden
- [x] **Allowlist entfernen**: `internal/config/rawobject_allowlist.go` + Tests, `OperatorConfig.RawObjectAllowlist`, `--raw-object-allowlist-file`-Flag, Allowlist-Gate + `RawObjectDenied`
- [x] **Helm**: ClusterRole `get`+`impersonate` auf serviceaccounts, Allowlist-ConfigMap/Mount/Arg/Checksum raus, optionales VAP-Template (`operator.rawObjects.authorCheck.enabled`, default `false`), Values
- [x] **Example**: `gnp-applier`-SA + ClusterRole/Binding an Tenant-SA, `serviceAccountName` im CR, README-Walkthrough
- [x] **Tests**: Unit (Factory-Seam, Spec-Validierung, SA fehlt, Cleanup-Identität) + Integration (envtest: Erfolg, Forbidden→Grant→grün, Finalizer via SA, SA fehlt→anlegen→grün)
- [x] **Doku**: README (Example 5, Spec-Referenz, Values-Tabelle, Security-Abschnitt), CLAUDE.md RawObject-Abschnitt
- [x] **Verifikation**: `make test`, `make test-integration`, `make lint`, `make gosec`, `helm lint`/`template` (beide VAP-Zustände)
