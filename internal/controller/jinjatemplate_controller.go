// Package controller implements the JinjaTemplate reconciler.
package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
	tmpl "github.com/guided-traffic/jinja-template-operator/internal/template"
)

const (
	// ConditionReady is the condition type for template rendering readiness.
	ConditionReady = "Ready"

	// ReasonRenderSuccess indicates the template was rendered successfully.
	ReasonRenderSuccess = "RenderSuccess"

	// ReasonRenderFailed indicates the template rendering failed.
	ReasonRenderFailed = "RenderFailed"

	// ReasonSourceResolutionFailed indicates a source could not be resolved.
	ReasonSourceResolutionFailed = "SourceResolutionFailed"

	// ReasonTemplateLoadFailed indicates the template could not be loaded.
	ReasonTemplateLoadFailed = "TemplateLoadFailed"

	// ReasonOutputFailed indicates the output resource could not be created/updated.
	ReasonOutputFailed = "OutputFailed"

	// ReasonInvalidSpec indicates the CR spec is invalid.
	ReasonInvalidSpec = "InvalidSpec"

	// ReasonOldOutputDeleted indicates the old output resource was deleted after a target change.
	ReasonOldOutputDeleted = "OldOutputDeleted"

	// LabelManagedBy is set on generated resources.
	LabelManagedBy = "jto.gtrfc.com/managed-by"

	// LabelJinjaTemplate is set on generated resources to reference the CR.
	LabelJinjaTemplate = "jto.gtrfc.com/jinja-template"

	// AnnotationTemplateHash stores the hash of the rendered template data.
	AnnotationTemplateHash = "jto.gtrfc.com/template-hash"

	// ManagerName is the name used for managed-by labels and event recording.
	ManagerName = "jinja-template-operator"

	// OutputKindConfigMap is the output kind for ConfigMap resources.
	OutputKindConfigMap = "ConfigMap"

	// OutputKindSecret is the output kind for Secret resources.
	OutputKindSecret = "Secret"
)

// JinjaTemplateReconciler reconciles JinjaTemplate objects.
type JinjaTemplateReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Config   *config.OperatorConfig
	Recorder events.EventRecorder
	Renderer *tmpl.Renderer
	Resolver *sources.Resolver
}

// Reconcile handles a single reconciliation for a JinjaTemplate CR.
func (r *JinjaTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the JinjaTemplate CR
	jt := &jtov1.JinjaTemplate{}
	if err := r.Get(ctx, req.NamespacedName, jt); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("JinjaTemplate resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get JinjaTemplate: %w", err)
	}

	// Reconcile and always update status at the end
	reconcileErr := r.reconcile(ctx, log, jt)

	// Update status
	if err := r.Status().Update(ctx, jt); err != nil {
		if apierrors.IsConflict(err) {
			log.V(1).Info("Conflict updating status, requeueing")
			return ctrl.Result{RequeueAfter: 1}, nil
		}
		log.Error(err, "Failed to update JinjaTemplate status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, reconcileErr
}

// reconcile performs the core reconciliation logic.
func (r *JinjaTemplateReconciler) reconcile(ctx context.Context, log logr.Logger, jt *jtov1.JinjaTemplate) error {
	// Validate spec
	if err := r.validateSpec(jt); err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonInvalidSpec, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonInvalidSpec, "Reconcile", "Invalid spec: %v", err)
		return nil // Don't requeue on validation errors
	}

	// Resolve sources
	log.V(1).Info("Resolving sources", "count", len(jt.Spec.Sources))
	templateContext, err := r.Resolver.Resolve(ctx, jt.Namespace, jt.Spec.Sources)
	if err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonSourceResolutionFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonSourceResolutionFailed, "Reconcile", "Source resolution failed: %v", err)
		return fmt.Errorf("source resolution failed: %w", err)
	}

	// Load template
	templateStr, err := r.loadTemplate(ctx, jt)
	if err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonTemplateLoadFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonTemplateLoadFailed, "Reconcile", "Template load failed: %v", err)
		return fmt.Errorf("template load failed: %w", err)
	}

	// Render template
	log.V(1).Info("Rendering template")
	rendered, err := r.Renderer.Render(templateStr, templateContext)
	if err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonRenderFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonRenderFailed, "Reconcile", "Template rendering failed: %v", err)
		return nil // Don't requeue on render errors
	}

	// Create or update output resource
	outputName := jt.Spec.Output.Name
	if outputName == "" {
		outputName = jt.Name
	}

	// Clean up old output if the target changed (different name or kind)
	if err := r.cleanupOldOutput(ctx, log, jt, outputName); err != nil {
		log.Error(err, "Failed to clean up old output resource")
		// Continue anyway — creating the new output is more important
	}

	if err := r.createOrUpdateOutput(ctx, log, jt, outputName, rendered); err != nil {
		r.setCondition(jt, metav1.ConditionFalse, ReasonOutputFailed, err.Error())
		r.Recorder.Eventf(jt, nil, corev1.EventTypeWarning, ReasonOutputFailed, "Reconcile", "Output creation/update failed: %v", err)
		return fmt.Errorf("output creation/update failed: %w", err)
	}

	// Record current output in status for future cleanup
	jt.Status.LastOutput = &jtov1.OutputRef{
		Kind: jt.Spec.Output.Kind,
		Name: outputName,
	}

	// Success
	r.setCondition(jt, metav1.ConditionTrue, ReasonRenderSuccess, "Template rendered successfully")
	r.Recorder.Eventf(jt, nil, corev1.EventTypeNormal, ReasonRenderSuccess, "Reconcile", "Template rendered and output updated successfully")
	log.Info("Successfully reconciled JinjaTemplate", "output", fmt.Sprintf("%s/%s", jt.Spec.Output.Kind, outputName))

	return nil
}

// validateSpec validates the JinjaTemplate spec.
func (r *JinjaTemplateReconciler) validateSpec(jt *jtov1.JinjaTemplate) error {
	hasInline := jt.Spec.Template != ""
	hasExternal := jt.Spec.TemplateFrom != nil && jt.Spec.TemplateFrom.ConfigMapRef != nil

	if !hasInline && !hasExternal {
		return fmt.Errorf("either spec.template or spec.templateFrom.configMapRef must be provided")
	}
	if hasInline && hasExternal {
		return fmt.Errorf("only one of spec.template or spec.templateFrom.configMapRef can be provided")
	}

	if jt.Spec.Output.Kind != OutputKindConfigMap && jt.Spec.Output.Kind != OutputKindSecret {
		return fmt.Errorf("spec.output.kind must be either 'ConfigMap' or 'Secret', got %q", jt.Spec.Output.Kind)
	}

	for _, src := range jt.Spec.Sources {
		if src.Name == "" {
			return fmt.Errorf("source name must not be empty")
		}
		if src.ConfigMap == nil && src.Secret == nil {
			return fmt.Errorf("source %q must specify either configMap or secret", src.Name)
		}
		if src.ConfigMap != nil && src.Secret != nil {
			return fmt.Errorf("source %q must specify either configMap or secret, not both", src.Name)
		}
	}

	return nil
}

// loadTemplate loads the template string from either inline or external reference.
func (r *JinjaTemplateReconciler) loadTemplate(ctx context.Context, jt *jtov1.JinjaTemplate) (string, error) {
	if jt.Spec.Template != "" {
		return jt.Spec.Template, nil
	}

	if jt.Spec.TemplateFrom != nil && jt.Spec.TemplateFrom.ConfigMapRef != nil {
		ref := jt.Spec.TemplateFrom.ConfigMapRef
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: jt.Namespace, Name: ref.Name}, cm); err != nil {
			return "", fmt.Errorf("failed to get template ConfigMap %s/%s: %w", jt.Namespace, ref.Name, err)
		}

		tplStr, ok := cm.Data[ref.Key]
		if !ok {
			return "", fmt.Errorf("key %q not found in template ConfigMap %s/%s", ref.Key, jt.Namespace, ref.Name)
		}
		return tplStr, nil
	}

	return "", fmt.Errorf("no template source configured")
}

// cleanupOldOutput deletes the previously created output resource if the output target
// (name or kind) has changed. This prevents orphaned resources when users update the CR.
func (r *JinjaTemplateReconciler) cleanupOldOutput(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	currentOutputName string,
) error {
	last := jt.Status.LastOutput
	if last == nil {
		return nil // No previous output recorded
	}

	// Check if the target changed
	if last.Kind == jt.Spec.Output.Kind && last.Name == currentOutputName {
		return nil // Same target, nothing to clean up
	}

	log.Info("Output target changed, deleting old output resource",
		"oldKind", last.Kind, "oldName", last.Name,
		"newKind", jt.Spec.Output.Kind, "newName", currentOutputName,
	)

	if err := r.deleteOldOutput(ctx, jt.Namespace, last.Kind, last.Name); err != nil {
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

// deleteOldOutput deletes a ConfigMap or Secret by kind and name in the given namespace.
func (r *JinjaTemplateReconciler) deleteOldOutput(ctx context.Context, namespace, kind, name string) error {
	switch kind {
	case OutputKindConfigMap:
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}
		return r.Delete(ctx, obj)
	case OutputKindSecret:
		obj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}
		return r.Delete(ctx, obj)
	default:
		return fmt.Errorf("unsupported output kind for deletion: %s", kind)
	}
}

// createOrUpdateOutput creates or updates the output ConfigMap or Secret.
func (r *JinjaTemplateReconciler) createOrUpdateOutput(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	outputName, rendered string,
) error {
	shouldSetOwner := r.Config.ShouldSetOwnerReference(jt.Spec.SetOwnerReference)

	outputLabels := map[string]string{
		LabelManagedBy:     ManagerName,
		LabelJinjaTemplate: jt.Name,
	}

	switch jt.Spec.Output.Kind {
	case OutputKindConfigMap:
		return r.createOrUpdateConfigMap(ctx, log, jt, outputName, rendered, outputLabels, shouldSetOwner)
	case OutputKindSecret:
		return r.createOrUpdateSecret(ctx, log, jt, outputName, rendered, outputLabels, shouldSetOwner)
	default:
		return fmt.Errorf("unsupported output kind: %s", jt.Spec.Output.Kind)
	}
}

// outputKey returns the data key for the rendered content.
// Defaults to "content" if not specified in the CR.
func outputKey(jt *jtov1.JinjaTemplate) string {
	if jt.Spec.Output.Key != "" {
		return jt.Spec.Output.Key
	}
	return "content"
}

// createOrUpdateConfigMap creates or updates a ConfigMap with the rendered content.
func (r *JinjaTemplateReconciler) createOrUpdateConfigMap(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	name, rendered string,
	outputLabels map[string]string,
	shouldSetOwner bool,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: jt.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = mergeLabels(cm.Labels, outputLabels)
		cm.Data = map[string]string{
			outputKey(jt): rendered,
		}

		if shouldSetOwner {
			return controllerutil.SetControllerReference(jt, cm, r.Scheme)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to %s ConfigMap %s/%s: %w", op, jt.Namespace, name, err)
	}

	log.V(1).Info("ConfigMap reconciled", "name", name, "operation", op)
	return nil
}

// createOrUpdateSecret creates or updates a Secret with the rendered content.
func (r *JinjaTemplateReconciler) createOrUpdateSecret(
	ctx context.Context,
	log logr.Logger,
	jt *jtov1.JinjaTemplate,
	name, rendered string,
	outputLabels map[string]string,
	shouldSetOwner bool,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: jt.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = mergeLabels(secret.Labels, outputLabels)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{
			outputKey(jt): []byte(rendered),
		}

		if shouldSetOwner {
			return controllerutil.SetControllerReference(jt, secret, r.Scheme)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to %s Secret %s/%s: %w", op, jt.Namespace, name, err)
	}

	log.V(1).Info("Secret reconciled", "name", name, "operation", op)
	return nil
}

// setCondition sets a condition on the JinjaTemplate status.
func (r *JinjaTemplateReconciler) setCondition(jt *jtov1.JinjaTemplate, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               ConditionReady,
		Status:             status,
		ObservedGeneration: jt.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	// Update or append the condition
	existingConditions := jt.Status.Conditions
	found := false
	for i, c := range existingConditions {
		if c.Type == ConditionReady {
			if c.Status != status || c.Reason != reason || c.Message != message {
				existingConditions[i] = condition
			}
			found = true
			break
		}
	}
	if !found {
		existingConditions = append(existingConditions, condition)
	}
	jt.Status.Conditions = existingConditions
}

// mergeLabels merges additional labels into existing labels without overwriting
// labels not managed by the operator.
func mergeLabels(existing, additional map[string]string) map[string]string {
	if existing == nil {
		existing = make(map[string]string)
	}
	for k, v := range additional {
		existing[k] = v
	}
	return existing
}

// SetupWithManager sets up the controller with the Manager.
func (r *JinjaTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&jtov1.JinjaTemplate{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.findJinjaTemplatesForConfigMap)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findJinjaTemplatesForSecret)).
		Complete(r)
}

// SetupWithManagerAndName sets up the controller with the Manager using a custom controller name.
// This is useful for integration tests where multiple controllers may run in the same process.
func (r *JinjaTemplateReconciler) SetupWithManagerAndName(mgr ctrl.Manager, name string) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&jtov1.JinjaTemplate{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.findJinjaTemplatesForConfigMap)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.findJinjaTemplatesForSecret)).
		Complete(r)
}

// findJinjaTemplatesForConfigMap maps ConfigMap events to JinjaTemplate reconcile requests.
func (r *JinjaTemplateReconciler) findJinjaTemplatesForConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.findJinjaTemplatesForObject(ctx, obj, OutputKindConfigMap)
}

// findJinjaTemplatesForSecret maps Secret events to JinjaTemplate reconcile requests.
func (r *JinjaTemplateReconciler) findJinjaTemplatesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.findJinjaTemplatesForObject(ctx, obj, OutputKindSecret)
}

// findJinjaTemplatesForObject finds all JinjaTemplates that reference the given object.
func (r *JinjaTemplateReconciler) findJinjaTemplatesForObject(ctx context.Context, obj client.Object, kind string) []reconcile.Request {
	log := ctrl.LoggerFrom(ctx)

	jtList := &jtov1.JinjaTemplateList{}
	if err := r.List(ctx, jtList, client.InNamespace(obj.GetNamespace())); err != nil {
		log.Error(err, "Failed to list JinjaTemplates for watch mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, jt := range jtList.Items {
		if r.objectReferencedByJinjaTemplate(&jt, obj, kind) || r.objectIsOutputOfJinjaTemplate(&jt, obj, kind) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      jt.Name,
					Namespace: jt.Namespace,
				},
			})
		}
	}

	if len(requests) > 0 {
		log.V(1).Info("Mapped object change to JinjaTemplates",
			"object", obj.GetName(),
			"kind", kind,
			"matchCount", len(requests),
		)
	}

	return requests
}

// objectIsOutputOfJinjaTemplate checks if the given object is the output target of the JinjaTemplate.
// This enables the operator to re-reconcile (and restore) the output when it is deleted or modified externally.
func (r *JinjaTemplateReconciler) objectIsOutputOfJinjaTemplate(jt *jtov1.JinjaTemplate, obj client.Object, kind string) bool {
	if jt.Spec.Output.Kind != kind {
		return false
	}
	outputName := jt.Spec.Output.Name
	if outputName == "" {
		outputName = jt.Name
	}
	return obj.GetName() == outputName
}

// objectReferencedByJinjaTemplate checks if the given object is referenced by the JinjaTemplate.
func (r *JinjaTemplateReconciler) objectReferencedByJinjaTemplate(jt *jtov1.JinjaTemplate, obj client.Object, kind string) bool {
	// Check if the object is the template source ConfigMap
	if kind == OutputKindConfigMap && jt.Spec.TemplateFrom != nil && jt.Spec.TemplateFrom.ConfigMapRef != nil {
		if jt.Spec.TemplateFrom.ConfigMapRef.Name == obj.GetName() {
			return true
		}
	}

	// Check sources
	for _, src := range jt.Spec.Sources {
		if kind == OutputKindConfigMap && src.ConfigMap != nil {
			if r.sourceMatchesObject(src.ConfigMap.Name, src.ConfigMap.LabelSelector, obj) {
				return true
			}
		}
		if kind == OutputKindSecret && src.Secret != nil {
			if r.sourceMatchesObject(src.Secret.Name, src.Secret.LabelSelector, obj) {
				return true
			}
		}
	}

	return false
}

// sourceMatchesObject checks if a source reference matches the given object.
func (r *JinjaTemplateReconciler) sourceMatchesObject(directName string, selector *metav1.LabelSelector, obj client.Object) bool {
	// Direct name match
	if directName != "" && directName == obj.GetName() {
		return true
	}

	// Label selector match
	if selector != nil {
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return false
		}
		return sel.Matches(labels.Set(obj.GetLabels()))
	}

	return false
}

// Ensure the reconciler implements the Reconciler interface.
var _ reconcile.Reconciler = &JinjaTemplateReconciler{}

// Ensure equality import is used (needed for future optimizations).
var _ = equality.Semantic
