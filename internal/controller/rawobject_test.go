package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
	tmpl "github.com/guided-traffic/jinja-template-operator/internal/template"
)

const (
	clusterRoleManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: raw-cluster-role
rules: []
`
	roleManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: raw-role
rules: []
`
)

// rbacAllowlist grants the "infra" namespace both test kinds (Role, ClusterRole).
func rbacAllowlist() []config.RawObjectAllowlistEntry {
	return []config.RawObjectAllowlistEntry{
		{
			Namespaces: []string{"infra"},
			Kinds: []config.RawObjectKind{
				{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
				{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole"},
			},
		},
	}
}

// newRawTestReconciler builds a reconciler backed by a fake client whose
// RESTMapper knows the scope of the rbac test kinds.
func newRawTestReconciler(allowlist []config.RawObjectAllowlistEntry, objs ...runtime.Object) *JinjaTemplateReconciler {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = jtov1.AddToScheme(scheme)

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(rbacv1.SchemeGroupVersion.WithKind("Role"), meta.RESTScopeNamespace)
	mapper.Add(rbacv1.SchemeGroupVersion.WithKind("ClusterRole"), meta.RESTScopeRoot)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&jtov1.JinjaTemplate{}).
		Build()

	cfg := config.NewOperatorConfig()
	cfg.RawObjectAllowlist = allowlist

	return &JinjaTemplateReconciler{
		Client:   c,
		Scheme:   scheme,
		Config:   cfg,
		Recorder: events.NewFakeRecorder(20),
		Renderer: tmpl.NewRenderer(),
		Resolver: sources.NewResolver(c),
	}
}

func rawJinjaTemplate(name, namespace, template string) *jtov1.JinjaTemplate {
	return &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: template,
			Output:   jtov1.Output{Kind: OutputKindRawObject},
		},
	}
}

func reconcileOnce(t *testing.T, r *JinjaTemplateReconciler, name, namespace string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
}

func getJinjaTemplate(t *testing.T, r *JinjaTemplateReconciler, name, namespace string) *jtov1.JinjaTemplate {
	t.Helper()
	jt := &jtov1.JinjaTemplate{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, jt))
	return jt
}

func readyCondition(jt *jtov1.JinjaTemplate) *metav1.Condition {
	for i := range jt.Status.Conditions {
		if jt.Status.Conditions[i].Type == ConditionReady {
			return &jt.Status.Conditions[i]
		}
	}
	return nil
}

func TestReconcileRawObjectClusterScoped(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Output object exists with operator labels
	cr := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr))
	assert.Equal(t, ManagerName, cr.Labels[LabelManagedBy])
	assert.Equal(t, "gnp", cr.Labels[LabelJinjaTemplate])
	assert.Empty(t, cr.OwnerReferences, "cluster-scoped output must not carry a namespaced owner reference")

	// CR carries the cleanup finalizer and records the output
	got := getJinjaTemplate(t, r, "gnp", "infra")
	assert.True(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	require.NotNil(t, got.Status.LastOutput)
	assert.Equal(t, "rbac.authorization.k8s.io/v1", got.Status.LastOutput.APIVersion)
	assert.Equal(t, "ClusterRole", got.Status.LastOutput.Kind)
	assert.Equal(t, "raw-cluster-role", got.Status.LastOutput.Name)
	assert.Empty(t, got.Status.LastOutput.Namespace)

	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcileRawObjectNamespaced(t *testing.T) {
	jt := rawJinjaTemplate("role-gen", "infra", roleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "role-gen", "infra")
	require.NoError(t, err)

	// Created in the CR's namespace with an owner reference, no finalizer
	role := &rbacv1.Role{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-role", Namespace: "infra"}, role))
	require.Len(t, role.OwnerReferences, 1)
	assert.Equal(t, "role-gen", role.OwnerReferences[0].Name)

	got := getJinjaTemplate(t, r, "role-gen", "infra")
	assert.False(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	require.NotNil(t, got.Status.LastOutput)
	assert.Equal(t, "infra", got.Status.LastOutput.Namespace)
}

func TestReconcileRawObjectNoOwnerReference(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	noOwner := false
	jt.Spec.SetOwnerReference = &noOwner
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "gnp", "infra")
	assert.False(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup),
		"setOwnerReference=false must not add the cleanup finalizer")
}

func TestReconcileRawObjectDenied(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "other", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "other")
	require.NoError(t, err)

	// No object created
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr))

	got := getJinjaTemplate(t, r, "gnp", "other")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonRawObjectDenied, cond.Reason)
	assert.Contains(t, cond.Message, "not allowed")
}

func TestReconcileRawObjectInvalidManifest(t *testing.T) {
	tests := []struct {
		name     string
		template string
		wantMsg  string
	}{
		{"not yaml", "{{ '{' }}invalid", "not a single valid YAML document"},
		{"scalar", "just-a-string", "not a single valid YAML document"},
		{"missing apiVersion", "kind: ClusterRole\nmetadata:\n  name: x\n", "must set apiVersion"},
		{"missing kind", "apiVersion: v1\nmetadata:\n  name: x\n", "must set kind"},
		{"missing name", "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {}\n", "must set metadata.name"},
		{
			"multi-doc",
			clusterRoleManifest + "---\n" + clusterRoleManifest,
			"not a single valid YAML document",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jt := rawJinjaTemplate("gnp", "infra", tt.template)
			r := newRawTestReconciler(rbacAllowlist(), jt)

			_, err := reconcileOnce(t, r, "gnp", "infra")
			require.NoError(t, err, "manifest problems must not requeue")

			got := getJinjaTemplate(t, r, "gnp", "infra")
			cond := readyCondition(got)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, ReasonRawObjectInvalid, cond.Reason)
			assert.Contains(t, cond.Message, tt.wantMsg)
		})
	}
}

func TestReconcileRawObjectCrossNamespaceDenied(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: raw-role
  namespace: other
rules: []
`
	jt := rawJinjaTemplate("role-gen", "infra", manifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "role-gen", "infra")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "role-gen", "infra")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonRawObjectInvalid, cond.Reason)
	assert.Contains(t, cond.Message, "CR's own namespace")

	role := &rbacv1.Role{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-role", Namespace: "other"}, role)
	assert.True(t, apierrors.IsNotFound(getErr))
}

func TestReconcileRawObjectClusterScopedWithNamespaceDenied(t *testing.T) {
	manifest := `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: raw-cluster-role
  namespace: infra
rules: []
`
	jt := rawJinjaTemplate("gnp", "infra", manifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "gnp", "infra")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonRawObjectInvalid, cond.Reason)
	assert.Contains(t, cond.Message, "cluster-scoped")
}

func TestReconcileRawObjectSpecValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*jtov1.JinjaTemplate)
	}{
		{"name set", func(jt *jtov1.JinjaTemplate) { jt.Spec.Output.Name = "explicit" }},
		{"key set", func(jt *jtov1.JinjaTemplate) { jt.Spec.Output.Key = "content" }},
		{"keys set", func(jt *jtov1.JinjaTemplate) {
			jt.Spec.Output.Keys = []jtov1.OutputKey{{Key: "a", Template: "x"}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
			tt.mutate(jt)
			r := newRawTestReconciler(rbacAllowlist(), jt)

			_, err := reconcileOnce(t, r, "gnp", "infra")
			require.NoError(t, err)

			got := getJinjaTemplate(t, r, "gnp", "infra")
			cond := readyCondition(got)
			require.NotNil(t, cond)
			assert.Equal(t, ReasonInvalidSpec, cond.Reason)
			assert.Contains(t, cond.Message, "must not be set for RawObject outputs")
		})
	}
}

func TestReconcileRawObjectUpdatesExisting(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Change the rendered content and reconcile again
	got := getJinjaTemplate(t, r, "gnp", "infra")
	got.Spec.Template = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: raw-cluster-role
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get"]
`
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr))
	require.Len(t, cr.Rules, 1)
	assert.Equal(t, []string{"pods"}, cr.Rules[0].Resources)
}

func TestReconcileRawObjectTargetChangeCleansUpOldObject(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Rename the rendered object
	got := getJinjaTemplate(t, r, "gnp", "infra")
	got.Spec.Template = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: renamed-cluster-role
rules: []
`
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	oldCR := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, oldCR)
	assert.True(t, apierrors.IsNotFound(getErr), "old raw output must be deleted after target change")

	newCR := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "renamed-cluster-role"}, newCR))
}

func TestReconcileRawObjectFinalizerCleanup(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Delete the CR: the finalizer keeps it around until the operator cleans up
	got := getJinjaTemplate(t, r, "gnp", "infra")
	require.NoError(t, r.Delete(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Raw output deleted, CR gone
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr), "raw output must be deleted on CR deletion")

	jtErr := r.Get(context.Background(), types.NamespacedName{Name: "gnp", Namespace: "infra"}, &jtov1.JinjaTemplate{})
	assert.True(t, apierrors.IsNotFound(jtErr), "CR must be released after finalizer cleanup")
}

func TestReconcileSwitchFromRawToConfigMap(t *testing.T) {
	jt := rawJinjaTemplate("gnp", "infra", clusterRoleManifest)
	r := newRawTestReconciler(rbacAllowlist(), jt)

	_, err := reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Switch the output to a ConfigMap
	got := getJinjaTemplate(t, r, "gnp", "infra")
	got.Spec.Template = "hello"
	got.Spec.Output = jtov1.Output{Kind: OutputKindConfigMap}
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp", "infra")
	require.NoError(t, err)

	// Old raw output deleted, finalizer dropped, ConfigMap created
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr))

	got = getJinjaTemplate(t, r, "gnp", "infra")
	assert.False(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	require.NotNil(t, got.Status.LastOutput)
	assert.Empty(t, got.Status.LastOutput.APIVersion)
	assert.Equal(t, OutputKindConfigMap, got.Status.LastOutput.Kind)

	cm := &corev1.ConfigMap{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "gnp", Namespace: "infra"}, cm))
	assert.Equal(t, "hello", cm.Data["content"])
}

func TestParseRawObjectGenerateNameRejected(t *testing.T) {
	_, err := parseRawObject(`apiVersion: v1
kind: ConfigMap
metadata:
  name: x
  generateName: x-
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generateName")
}
