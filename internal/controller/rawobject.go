package controller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
)

const (
	// OutputKindRawObject is the output kind for arbitrary Kubernetes objects.
	OutputKindRawObject = "RawObject"

	// FinalizerRawOutputCleanup marks CRs whose raw output must be deleted by
	// the operator before the CR itself can go away. Used for cluster-scoped
	// raw outputs, where a namespaced OwnerReference is not permitted.
	FinalizerRawOutputCleanup = "jto.gtrfc.com/raw-output-cleanup"

	// ReasonRawObjectInvalid indicates the rendered output is not a valid
	// single-object Kubernetes manifest.
	ReasonRawObjectInvalid = "RawObjectInvalid"

	// ReasonServiceAccountNotFound indicates the ServiceAccount named in
	// spec.output.serviceAccountName does not exist in the CR's namespace.
	ReasonServiceAccountNotFound = "ServiceAccountNotFound"

	// ReasonOutputForbidden indicates the impersonated ServiceAccount lacks
	// RBAC permission to apply the rendered object.
	ReasonOutputForbidden = "OutputForbidden"

	// ReasonFinalizeForbidden indicates the raw output could not be deleted
	// during CR finalization because the ServiceAccount is missing or lacks
	// RBAC permission.
	ReasonFinalizeForbidden = "FinalizeForbidden"
)

// validateRawObjectOutputSpec enforces the spec constraints specific to
// RawObject outputs.
func validateRawObjectOutputSpec(output jtov1.Output) error {
	if output.Name != "" {
		return fmt.Errorf("spec.output.name must not be set for RawObject outputs (the name comes from the rendered manifest)")
	}
	if output.Key != "" {
		return fmt.Errorf("spec.output.key must not be set for RawObject outputs")
	}
	if len(output.Keys) > 0 {
		return fmt.Errorf("spec.output.keys must not be set for RawObject outputs")
	}
	if output.ServiceAccountName == "" {
		return fmt.Errorf("spec.output.serviceAccountName is required for RawObject outputs (the operator applies the object as this ServiceAccount)")
	}
	return nil
}

// reconcileRawObjectOutput handles the output phase for RawObject outputs:
// parse the rendered manifest, verify the impersonation target exists,
// enforce the namespace rules, manage the cleanup finalizer, clean up a
// previous output if the target changed, and apply the object via
// server-side apply under the ServiceAccount's identity.
func (r *JinjaTemplateReconciler) reconcileRawObjectOutput(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	rendered string,
	dnsRequeue time.Duration,
) (ctrl.Result, error) {
	obj, err := parseRawObject(rendered)
	if err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonRawObjectInvalid, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonRawObjectInvalid, "Reconcile", "Invalid raw object: %v", err)
		return ctrl.Result{}, nil // Manifest problems are not fixed by requeueing
	}

	saName := jt.Spec.Output.ServiceAccountName
	if err := r.checkServiceAccount(ctx, jt, saName); err != nil {
		return ctrl.Result{}, err
	}

	namespaced, err := r.isNamespacedKind(obj.GroupVersionKind())
	if err != nil {
		// Discovery failure or missing CRD — may resolve later, so requeue.
		r.setCondition(jt, metav1.ConditionFalse, ReasonOutputFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Reconcile", "Raw object type lookup failed: %v", err)
		return ctrl.Result{}, fmt.Errorf("raw object type lookup failed: %w", err)
	}

	if err := validateRawObjectNamespace(jt, obj, namespaced); err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonRawObjectInvalid, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonRawObjectInvalid, "Reconcile", "Invalid raw object: %v", err)
		return ctrl.Result{}, nil
	}

	// The finalizer must be in place before the object exists, otherwise a CR
	// deletion racing the apply would orphan a cluster-scoped output.
	shouldSetOwner := r.Config.ShouldSetOwnerReference(jt.Spec.SetOwnerReference)
	needsFinalizer := shouldSetOwner && !namespaced
	if err := r.reconcileRawOutputFinalizer(ctx, jt, needsFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	newRef := rawOutputRef(obj)
	newRef.ServiceAccountName = saName
	if err := r.cleanupOldRawOutput(ctx, log, jt, newRef); err != nil {
		log.Error(err, "Failed to clean up old output resource")
		// Continue anyway — creating the new output is more important
	}

	if err := r.applyRawObject(ctx, log, jt, obj, namespaced, shouldSetOwner); err != nil {
		if apierrors.IsForbidden(err) {
			identity := serviceAccountUserName(jt.Namespace, saName)
			msg := fmt.Sprintf(
				"apply of %s %q as %s was denied: %v. Grant the ServiceAccount get/create/patch on the target kind and verify with: kubectl auth can-i create %s --as=%s",
				obj.GetKind(), obj.GetName(), identity, err, r.canIResource(obj.GetAPIVersion(), obj.GetKind()), identity,
			)
			r.setCondition(jt, metav1.ConditionFalse, ReasonOutputForbidden, msg)
			r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputForbidden, "Reconcile", "%s", msg)
			// RBAC grants do not trigger a reconcile, so a denial must not be
			// terminal — return the error for backoff-requeue.
			return ctrl.Result{}, fmt.Errorf("output apply forbidden: %w", err)
		}
		r.setCondition(jt, metav1.ConditionFalse, ReasonOutputFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Reconcile", "Output creation/update failed: %v", err)
		return ctrl.Result{}, fmt.Errorf("output creation/update failed: %w", err)
	}

	jt.Status.LastOutput = newRef

	r.setCondition(jt, metav1.ConditionTrue, ReasonRenderSuccess, "Template rendered successfully")
	r.Recorder.Eventf(jt, nil, corev1.EventTypeNormal, ReasonRenderSuccess, "Reconcile", "Template rendered and output updated successfully")
	log.Info("Successfully reconciled JinjaTemplate", "output", fmt.Sprintf("%s/%s", obj.GetKind(), obj.GetName()))

	return ctrl.Result{RequeueAfter: dnsRequeue}, nil
}

// parseRawObject parses the rendered template as a single-document Kubernetes
// manifest and validates the fields the operator depends on.
func parseRawObject(rendered string) (*unstructured.Unstructured, error) {
	doc, err := singleYAMLDocument(rendered)
	if err != nil {
		return nil, err
	}

	var content map[string]interface{}
	if err := yaml.UnmarshalStrict(doc, &content); err != nil {
		return nil, fmt.Errorf("rendered output is not a single valid YAML document: %w", err)
	}
	if content == nil {
		return nil, fmt.Errorf("rendered output is empty")
	}

	obj := &unstructured.Unstructured{Object: content}
	if obj.GetAPIVersion() == "" {
		return nil, fmt.Errorf("rendered manifest must set apiVersion")
	}
	if obj.GetKind() == "" {
		return nil, fmt.Errorf("rendered manifest must set kind")
	}
	if obj.GetName() == "" {
		return nil, fmt.Errorf("rendered manifest must set metadata.name")
	}
	if obj.GetGenerateName() != "" {
		return nil, fmt.Errorf("rendered manifest must not set metadata.generateName")
	}
	return obj, nil
}

// singleYAMLDocument returns the only non-empty YAML document in the rendered
// output, or an error if there are several. sigs.k8s.io/yaml would otherwise
// silently drop everything after the first document separator.
func singleYAMLDocument(rendered string) ([]byte, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(rendered)))
	var docs [][]byte
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("rendered output is not a single valid YAML document: %w", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("rendered output is empty")
	}
	if len(docs) > 1 {
		return nil, fmt.Errorf("rendered output is not a single valid YAML document: found %d documents", len(docs))
	}
	return docs[0], nil
}

// isNamespacedKind reports whether the given GVK is namespace-scoped,
// using the client's RESTMapper (backed by API discovery).
func (r *JinjaTemplateReconciler) isNamespacedKind(gvk schema.GroupVersionKind) (bool, error) {
	mapping, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, fmt.Errorf("unknown object type %s (is the CRD installed?): %w", gvk, err)
	}
	return mapping.Scope.Name() == meta.RESTScopeNameNamespace, nil
}

// validateRawObjectNamespace enforces that namespaced raw outputs stay in the
// CR's own namespace and cluster-scoped ones set no namespace at all. On
// success the target namespace is filled in for namespaced kinds.
func validateRawObjectNamespace(jt *jtov1.JinjaTemplate, obj *unstructured.Unstructured, namespaced bool) error {
	ns := obj.GetNamespace()
	if namespaced {
		if ns != "" && ns != jt.Namespace {
			return fmt.Errorf(
				"rendered manifest targets namespace %q, but RawObject outputs may only be written to the CR's own namespace %q",
				ns, jt.Namespace,
			)
		}
		obj.SetNamespace(jt.Namespace)
		return nil
	}
	if ns != "" {
		return fmt.Errorf("rendered manifest sets metadata.namespace %q, but %s is cluster-scoped", ns, obj.GetKind())
	}
	return nil
}

// applyRawObject creates or updates the rendered object via server-side apply,
// acting as the CR's output ServiceAccount. Namespaced objects get an
// OwnerReference when requested; cluster-scoped objects rely on the
// finalizer-based cleanup instead.
func (r *JinjaTemplateReconciler) applyRawObject(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	obj *unstructured.Unstructured,
	namespaced bool,
	shouldSetOwner bool,
) error {
	obj.SetLabels(mergeLabels(obj.GetLabels(), map[string]string{
		LabelManagedBy:     ManagerName,
		LabelJinjaTemplate: jt.Name,
	}))

	if namespaced && shouldSetOwner {
		if err := controllerutil.SetControllerReference(jt, obj, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}

	c, err := r.rawClientFor(jt.Namespace, jt.Spec.Output.ServiceAccountName)
	if err != nil {
		return fmt.Errorf("failed to build impersonated client: %w", err)
	}

	applyConfig := client.ApplyConfigurationFromUnstructured(obj)
	if err := c.Apply(ctx, applyConfig, client.ForceOwnership, client.FieldOwner(ManagerName)); err != nil {
		return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}

	log.V(1).Info("Raw object applied", "kind", obj.GetKind(), "name", obj.GetName(), "namespaced", namespaced)
	return nil
}

// rawClientFor builds a client whose requests impersonate the given
// ServiceAccount. Discovery and REST mapping stay with the operator's own
// identity; only object access runs as the ServiceAccount. The client is
// built per reconcile and not cached — client-go pools the underlying
// transport, so this is cheap.
func (r *JinjaTemplateReconciler) rawClientFor(namespace, serviceAccountName string) (client.Client, error) {
	if r.RawClientFactory != nil {
		return r.RawClientFactory(namespace, serviceAccountName)
	}
	cfg := rest.CopyConfig(r.RestConfig)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: serviceAccountUserName(namespace, serviceAccountName),
	}
	return client.New(cfg, client.Options{Scheme: r.Scheme, Mapper: r.RESTMapper()})
}

// serviceAccountUserName returns the Kubernetes user name of a ServiceAccount.
func serviceAccountUserName(namespace, name string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, name)
}

// checkServiceAccount verifies fail-closed that the impersonation target
// exists, reading uncached straight from the API server. RBAC bindings match
// the bare name string and keep authorizing a deleted ServiceAccount, so a
// missing ServiceAccount must behave like a revocation. Errors are returned
// for backoff-requeue: ServiceAccounts are not watched, so recreation does
// not trigger a reconcile.
func (r *JinjaTemplateReconciler) checkServiceAccount(ctx context.Context, jt *jtov1.JinjaTemplate, saName string) error {
	sa := &corev1.ServiceAccount{}
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: jt.Namespace, Name: saName}, sa)
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		msg := fmt.Sprintf(
			"ServiceAccount %q not found in namespace %q; RawObject outputs are applied as this ServiceAccount",
			saName, jt.Namespace,
		)
		r.setCondition(jt, metav1.ConditionFalse, ReasonServiceAccountNotFound, msg)
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonServiceAccountNotFound, "Reconcile", "%s", msg)
		return fmt.Errorf("%s", msg)
	}
	r.setCondition(jt, metav1.ConditionFalse, ReasonOutputFailed, err.Error())
	r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Reconcile", "ServiceAccount lookup failed: %v", err)
	return fmt.Errorf("failed to get ServiceAccount %s/%s: %w", jt.Namespace, saName, err)
}

// canIResource returns "<resource>.<group>" for kubectl auth can-i
// remediation hints, falling back to the lowercased kind when the REST
// mapping is unavailable.
func (r *JinjaTemplateReconciler) canIResource(apiVersion, kind string) string {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return strings.ToLower(kind)
	}
	mapping, err := r.RESTMapper().RESTMapping(schema.GroupKind{Group: gv.Group, Kind: kind}, gv.Version)
	if err != nil {
		return strings.ToLower(kind)
	}
	if mapping.Resource.Group == "" {
		return mapping.Resource.Resource
	}
	return mapping.Resource.Resource + "." + mapping.Resource.Group
}

// cleanupOldRawOutput deletes the previously created output if the raw output
// target (GVK, name or namespace) changed, including a previous ConfigMap or
// Secret output after a switch to RawObject.
func (r *JinjaTemplateReconciler) cleanupOldRawOutput(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	newRef *jtov1.OutputRef,
) error {
	last := jt.Status.LastOutput
	if last == nil || sameOutputRef(last, newRef) {
		return nil
	}

	log.Info("Output target changed, deleting old output resource",
		"oldKind", last.Kind, "oldName", last.Name,
		"newKind", newRef.Kind, "newName", newRef.Name,
	)

	if err := r.deleteLastOutput(ctx, jt, last); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Old output resource already deleted", "kind", last.Kind, "name", last.Name)
			return nil
		}
		return fmt.Errorf("failed to delete old output %s/%s: %w", last.Kind, last.Name, err)
	}

	r.Recorder.Eventf(jt, nil, corev1.EventTypeNormal, ReasonOldOutputDeleted, "Reconcile",
		"Deleted old output %s/%s after target change", last.Kind, last.Name)

	return nil
}

// deleteLastOutput deletes the resource recorded in an OutputRef, dispatching
// between the legacy ConfigMap/Secret form and the raw object form.
func (r *JinjaTemplateReconciler) deleteLastOutput(ctx context.Context, jt *jtov1.JinjaTemplate, ref *jtov1.OutputRef) error {
	if ref.APIVersion == "" {
		return r.deleteOldOutput(ctx, jt.Namespace, ref.Kind, ref.Name)
	}
	return r.deleteRawOutput(ctx, jt, ref)
}

// deleteRawOutput deletes a raw output object identified by an OutputRef,
// acting as the ServiceAccount that created it (falling back to the current
// spec ServiceAccount for status records written before that field existed).
func (r *JinjaTemplateReconciler) deleteRawOutput(ctx context.Context, jt *jtov1.JinjaTemplate, ref *jtov1.OutputRef) error {
	saName := ref.ServiceAccountName
	if saName == "" {
		saName = jt.Spec.Output.ServiceAccountName
	}
	if saName == "" {
		return fmt.Errorf(
			"cannot delete raw output %s %s: no ServiceAccount identity recorded in status and none set in spec.output.serviceAccountName",
			ref.Kind, ref.Name,
		)
	}

	c, err := r.rawClientFor(jt.Namespace, saName)
	if err != nil {
		return fmt.Errorf("failed to build impersonated client: %w", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ref.APIVersion)
	obj.SetKind(ref.Kind)
	obj.SetName(ref.Name)
	obj.SetNamespace(ref.Namespace)
	return c.Delete(ctx, obj)
}

// reconcileRawOutputFinalizer ensures the cleanup finalizer is present exactly
// when the operator is responsible for deleting a cluster-scoped raw output
// (RawObject output, owner-reference semantics enabled, cluster-scoped kind).
func (r *JinjaTemplateReconciler) reconcileRawOutputFinalizer(
	ctx context.Context,
	jt *jtov1.JinjaTemplate,
	needsFinalizer bool,
) error {
	var changed bool
	if needsFinalizer {
		changed = controllerutil.AddFinalizer(jt, FinalizerRawOutputCleanup)
	} else {
		changed = controllerutil.RemoveFinalizer(jt, FinalizerRawOutputCleanup)
	}
	if !changed {
		return nil
	}
	if err := r.Update(ctx, jt); err != nil {
		return fmt.Errorf("failed to update finalizers: %w", err)
	}
	return nil
}

// finalizeRawOutput handles CR deletion: removes the recorded raw output (if
// any) under the creating ServiceAccount's identity and drops the cleanup
// finalizer so the CR can be deleted. A missing ServiceAccount or an RBAC
// denial keeps the CR in Terminating; the documented remedies are restoring
// the ServiceAccount, re-granting RBAC, or removing the finalizer manually.
func (r *JinjaTemplateReconciler) finalizeRawOutput(ctx context.Context, log logr.Logger, jt *jtov1.JinjaTemplate) error {
	if !controllerutil.ContainsFinalizer(jt, FinalizerRawOutputCleanup) {
		return nil
	}

	last := jt.Status.LastOutput
	if last != nil && last.APIVersion != "" {
		if err := r.finalizeDeleteRawOutput(ctx, jt, last); err != nil {
			return err
		}
		log.Info("Deleted raw output on CR deletion", "kind", last.Kind, "name", last.Name)
	}

	controllerutil.RemoveFinalizer(jt, FinalizerRawOutputCleanup)
	if err := r.Update(ctx, jt); err != nil {
		return fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return nil
}

// finalizeDeleteRawOutput deletes the recorded raw output during CR
// finalization, fail-closed on the ServiceAccount existence check and with
// remediation hints on RBAC denials.
func (r *JinjaTemplateReconciler) finalizeDeleteRawOutput(ctx context.Context, jt *jtov1.JinjaTemplate, last *jtov1.OutputRef) error {
	saName := last.ServiceAccountName
	if saName == "" {
		saName = jt.Spec.Output.ServiceAccountName
	}

	if saName != "" {
		sa := &corev1.ServiceAccount{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: jt.Namespace, Name: saName}, sa); err != nil {
			r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonFinalizeForbidden, "Finalize",
				"Cannot delete raw output %s %s: ServiceAccount %q lookup failed: %v. Restore the ServiceAccount, or remove the finalizer %q manually to abandon the output",
				last.Kind, last.Name, saName, err, FinalizerRawOutputCleanup)
			return fmt.Errorf("failed to get ServiceAccount %s/%s for finalization: %w", jt.Namespace, saName, err)
		}
	}

	err := r.deleteRawOutput(ctx, jt, last)
	if err == nil || apierrors.IsNotFound(err) {
		return nil
	}
	if apierrors.IsForbidden(err) {
		identity := serviceAccountUserName(jt.Namespace, saName)
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonFinalizeForbidden, "Finalize",
			"Deletion of raw output %s %s as %s was denied: %v. Re-grant delete on the target kind (verify with: kubectl auth can-i delete %s --as=%s), or remove the finalizer %q manually to abandon the output",
			last.Kind, last.Name, identity, err, r.canIResource(last.APIVersion, last.Kind), identity, FinalizerRawOutputCleanup)
		return fmt.Errorf("failed to delete raw output %s %s: %w", last.Kind, last.Name, err)
	}
	r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Finalize",
		"Failed to delete raw output %s %s: %v", last.Kind, last.Name, err)
	return fmt.Errorf("failed to delete raw output %s %s: %w", last.Kind, last.Name, err)
}

// rawOutputRef builds the OutputRef recorded in status for a raw output.
func rawOutputRef(obj *unstructured.Unstructured) *jtov1.OutputRef {
	return &jtov1.OutputRef{
		APIVersion: obj.GetAPIVersion(),
		Kind:       obj.GetKind(),
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
	}
}

// sameOutputRef reports whether two output references identify the same
// object. The ServiceAccount identity is deliberately not compared: a
// ServiceAccount change alone re-applies the object under the new identity
// instead of deleting and recreating it.
func sameOutputRef(a, b *jtov1.OutputRef) bool {
	return a.APIVersion == b.APIVersion && a.Kind == b.Kind && a.Name == b.Name && a.Namespace == b.Namespace
}
