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

	// ReasonRawObjectDenied indicates the rendered object kind is not allowed
	// for the CR's namespace by the operator's allowlist.
	ReasonRawObjectDenied = "RawObjectDenied"
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
	return nil
}

// reconcileRawObjectOutput handles the output phase for RawObject outputs:
// parse the rendered manifest, enforce the allowlist and namespace rules,
// manage the cleanup finalizer, clean up a previous output if the target
// changed, and apply the object via server-side apply.
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

	if !r.Config.IsRawObjectAllowed(jt.Namespace, obj.GetAPIVersion(), obj.GetKind()) {
		msg := fmt.Sprintf(
			"namespace %q is not allowed to render RawObject outputs of %s/%s; grant it in the operator's raw object allowlist",
			jt.Namespace, obj.GetAPIVersion(), obj.GetKind(),
		)
		r.setCondition(jt, metav1.ConditionFalse, ReasonRawObjectDenied, msg)
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonRawObjectDenied, "Reconcile", "%s", msg)
		return ctrl.Result{}, nil // Allowlist changes roll the operator, which re-reconciles
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
	if err := r.cleanupOldRawOutput(ctx, log, jt, newRef); err != nil {
		log.Error(err, "Failed to clean up old output resource")
		// Continue anyway — creating the new output is more important
	}

	if err := r.applyRawObject(ctx, log, jt, obj, namespaced, shouldSetOwner); err != nil {
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

// applyRawObject creates or updates the rendered object via server-side apply.
// Namespaced objects get an OwnerReference when requested; cluster-scoped
// objects rely on the finalizer-based cleanup instead.
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

	applyConfig := client.ApplyConfigurationFromUnstructured(obj)
	if err := r.Apply(ctx, applyConfig, client.ForceOwnership, client.FieldOwner(ManagerName)); err != nil {
		return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}

	log.V(1).Info("Raw object applied", "kind", obj.GetKind(), "name", obj.GetName(), "namespaced", namespaced)
	return nil
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
	return r.deleteRawOutput(ctx, ref)
}

// deleteRawOutput deletes a raw output object identified by an OutputRef.
func (r *JinjaTemplateReconciler) deleteRawOutput(ctx context.Context, ref *jtov1.OutputRef) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ref.APIVersion)
	obj.SetKind(ref.Kind)
	obj.SetName(ref.Name)
	obj.SetNamespace(ref.Namespace)
	return r.Delete(ctx, obj)
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
// any) and drops the cleanup finalizer so the CR can be deleted.
func (r *JinjaTemplateReconciler) finalizeRawOutput(ctx context.Context, log logr.Logger, jt *jtov1.JinjaTemplate) error {
	if !controllerutil.ContainsFinalizer(jt, FinalizerRawOutputCleanup) {
		return nil
	}

	last := jt.Status.LastOutput
	if last != nil && last.APIVersion != "" {
		if err := r.deleteRawOutput(ctx, last); err != nil && !apierrors.IsNotFound(err) {
			r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Finalize",
				"Failed to delete raw output %s %s: %v", last.Kind, last.Name, err)
			return fmt.Errorf("failed to delete raw output %s %s: %w", last.Kind, last.Name, err)
		}
		log.Info("Deleted raw output on CR deletion", "kind", last.Kind, "name", last.Name)
	}

	controllerutil.RemoveFinalizer(jt, FinalizerRawOutputCleanup)
	if err := r.Update(ctx, jt); err != nil {
		return fmt.Errorf("failed to remove finalizer: %w", err)
	}
	return nil
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

// sameOutputRef reports whether two output references identify the same object.
func sameOutputRef(a, b *jtov1.OutputRef) bool {
	return a.APIVersion == b.APIVersion && a.Kind == b.Kind && a.Name == b.Name && a.Namespace == b.Namespace
}
