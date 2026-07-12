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
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	// rawSAName is the ServiceAccount the raw object tests impersonate.
	rawSAName = "raw-applier"

	// rawTestNS is the namespace all raw object tests run in.
	rawTestNS = "infra"
)

// serviceAccountObj returns a ServiceAccount fixture for the fake client.
func serviceAccountObj(name string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: rawTestNS},
	}
}

// newRawTestReconciler builds a reconciler backed by a fake client whose
// RESTMapper knows the scope of the rbac test kinds. The fake client cannot
// simulate impersonation, so the RawClientFactory seam returns the fake
// client itself and records the requested identities ("namespace/name") for
// assertions.
func newRawTestReconciler(objs ...runtime.Object) (*JinjaTemplateReconciler, *[]string) {
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

	impersonated := &[]string{}

	r := &JinjaTemplateReconciler{
		Client:    c,
		Scheme:    scheme,
		Config:    config.NewOperatorConfig(),
		Recorder:  events.NewFakeRecorder(20),
		Renderer:  tmpl.NewRenderer(),
		Resolver:  sources.NewResolver(c),
		APIReader: c,
		RawClientFactory: func(namespace, serviceAccountName string) (client.Client, error) {
			*impersonated = append(*impersonated, namespace+"/"+serviceAccountName)
			return c, nil
		},
	}
	return r, impersonated
}

func rawJinjaTemplate(name, template string) *jtov1.JinjaTemplate {
	return &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: rawTestNS,
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: template,
			Output: jtov1.Output{
				Kind:               OutputKindRawObject,
				ServiceAccountName: rawSAName,
			},
		},
	}
}

func reconcileOnce(t *testing.T, r *JinjaTemplateReconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: rawTestNS},
	})
}

func getJinjaTemplate(t *testing.T, r *JinjaTemplateReconciler, name string) *jtov1.JinjaTemplate {
	t.Helper()
	jt := &jtov1.JinjaTemplate{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: rawTestNS}, jt))
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
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, impersonated := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Output object exists with operator labels
	cr := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr))
	assert.Equal(t, ManagerName, cr.Labels[LabelManagedBy])
	assert.Equal(t, "gnp", cr.Labels[LabelJinjaTemplate])
	assert.Empty(t, cr.OwnerReferences, "cluster-scoped output must not carry a namespaced owner reference")

	// Apply ran under the CR's ServiceAccount identity
	assert.Equal(t, []string{"infra/" + rawSAName}, *impersonated)

	// CR carries the cleanup finalizer and records the output incl. identity
	got := getJinjaTemplate(t, r, "gnp")
	assert.True(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	require.NotNil(t, got.Status.LastOutput)
	assert.Equal(t, "rbac.authorization.k8s.io/v1", got.Status.LastOutput.APIVersion)
	assert.Equal(t, "ClusterRole", got.Status.LastOutput.Kind)
	assert.Equal(t, "raw-cluster-role", got.Status.LastOutput.Name)
	assert.Empty(t, got.Status.LastOutput.Namespace)
	assert.Equal(t, rawSAName, got.Status.LastOutput.ServiceAccountName)

	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcileRawObjectNamespaced(t *testing.T) {
	jt := rawJinjaTemplate("role-gen", roleManifest)
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "role-gen")
	require.NoError(t, err)

	// Created in the CR's namespace with an owner reference, no finalizer
	role := &rbacv1.Role{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-role", Namespace: "infra"}, role))
	require.Len(t, role.OwnerReferences, 1)
	assert.Equal(t, "role-gen", role.OwnerReferences[0].Name)

	got := getJinjaTemplate(t, r, "role-gen")
	assert.False(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	require.NotNil(t, got.Status.LastOutput)
	assert.Equal(t, "infra", got.Status.LastOutput.Namespace)
}

func TestReconcileRawObjectNoOwnerReference(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	noOwner := false
	jt.Spec.SetOwnerReference = &noOwner
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "gnp")
	assert.False(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup),
		"setOwnerReference=false must not add the cleanup finalizer")
}

func TestReconcileRawObjectServiceAccountMissing(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, impersonated := newRawTestReconciler(jt) // no ServiceAccount fixture

	_, err := reconcileOnce(t, r, "gnp")
	require.Error(t, err, "a missing ServiceAccount must requeue with backoff")

	// No object created, nothing impersonated
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr))
	assert.Empty(t, *impersonated)

	got := getJinjaTemplate(t, r, "gnp")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonServiceAccountNotFound, cond.Reason)
	assert.Contains(t, cond.Message, rawSAName)
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
			jt := rawJinjaTemplate("gnp", tt.template)
			r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

			_, err := reconcileOnce(t, r, "gnp")
			require.NoError(t, err, "manifest problems must not requeue")

			got := getJinjaTemplate(t, r, "gnp")
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
	jt := rawJinjaTemplate("role-gen", manifest)
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "role-gen")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "role-gen")
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
	jt := rawJinjaTemplate("gnp", manifest)
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "gnp")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonRawObjectInvalid, cond.Reason)
	assert.Contains(t, cond.Message, "cluster-scoped")
}

func TestReconcileRawObjectSpecValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*jtov1.JinjaTemplate)
		wantMsg string
	}{
		{"name set", func(jt *jtov1.JinjaTemplate) { jt.Spec.Output.Name = "explicit" },
			"must not be set for RawObject outputs"},
		{"key set", func(jt *jtov1.JinjaTemplate) { jt.Spec.Output.Key = "content" },
			"must not be set for RawObject outputs"},
		{"keys set", func(jt *jtov1.JinjaTemplate) {
			jt.Spec.Output.Keys = []jtov1.OutputKey{{Key: "a", Template: "x"}}
		}, "must not be set for RawObject outputs"},
		{"serviceAccountName missing", func(jt *jtov1.JinjaTemplate) {
			jt.Spec.Output.ServiceAccountName = ""
		}, "serviceAccountName is required for RawObject outputs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jt := rawJinjaTemplate("gnp", clusterRoleManifest)
			tt.mutate(jt)
			r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

			_, err := reconcileOnce(t, r, "gnp")
			require.NoError(t, err)

			got := getJinjaTemplate(t, r, "gnp")
			cond := readyCondition(got)
			require.NotNil(t, cond)
			assert.Equal(t, ReasonInvalidSpec, cond.Reason)
			assert.Contains(t, cond.Message, tt.wantMsg)
		})
	}
}

func TestReconcileServiceAccountNameForbiddenForConfigMap(t *testing.T) {
	jt := rawJinjaTemplate("cm-gen", "hello")
	jt.Spec.Output.Kind = OutputKindConfigMap
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "cm-gen")
	require.NoError(t, err)

	got := getJinjaTemplate(t, r, "cm-gen")
	cond := readyCondition(got)
	require.NotNil(t, cond)
	assert.Equal(t, ReasonInvalidSpec, cond.Reason)
	assert.Contains(t, cond.Message, "may only be set for RawObject outputs")
}

func TestReconcileRawObjectUpdatesExisting(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Change the rendered content and reconcile again
	got := getJinjaTemplate(t, r, "gnp")
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

	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	cr := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr))
	require.Len(t, cr.Rules, 1)
	assert.Equal(t, []string{"pods"}, cr.Rules[0].Resources)
}

func TestReconcileRawObjectTargetChangeCleansUpOldObject(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, _ := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Rename the rendered object
	got := getJinjaTemplate(t, r, "gnp")
	got.Spec.Template = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: renamed-cluster-role
rules: []
`
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	oldCR := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, oldCR)
	assert.True(t, apierrors.IsNotFound(getErr), "old raw output must be deleted after target change")

	newCR := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "renamed-cluster-role"}, newCR))
}

func TestReconcileRawObjectCleanupUsesLastOutputServiceAccount(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, impersonated := newRawTestReconciler(jt,
		serviceAccountObj(rawSAName),
		serviceAccountObj("new-applier"),
	)

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Rename the rendered object AND switch the ServiceAccount: the old
	// object must be deleted under the identity that created it.
	got := getJinjaTemplate(t, r, "gnp")
	got.Spec.Template = `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: renamed-cluster-role
rules: []
`
	got.Spec.Output.ServiceAccountName = "new-applier"
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	require.Len(t, *impersonated, 3)
	assert.Equal(t, "infra/"+rawSAName, (*impersonated)[0], "initial apply")
	assert.Equal(t, "infra/"+rawSAName, (*impersonated)[1], "cleanup must use the creator identity from status.lastOutput")
	assert.Equal(t, "infra/new-applier", (*impersonated)[2], "apply must use the current spec identity")

	got = getJinjaTemplate(t, r, "gnp")
	require.NotNil(t, got.Status.LastOutput)
	assert.Equal(t, "new-applier", got.Status.LastOutput.ServiceAccountName)
}

func TestReconcileRawObjectFinalizerCleanup(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, impersonated := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Delete the CR: the finalizer keeps it around until the operator cleans up
	got := getJinjaTemplate(t, r, "gnp")
	require.NoError(t, r.Delete(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Raw output deleted under the creating identity, CR gone
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr), "raw output must be deleted on CR deletion")
	assert.Equal(t, []string{"infra/" + rawSAName, "infra/" + rawSAName}, *impersonated)

	jtErr := r.Get(context.Background(), types.NamespacedName{Name: "gnp", Namespace: "infra"}, &jtov1.JinjaTemplate{})
	assert.True(t, apierrors.IsNotFound(jtErr), "CR must be released after finalizer cleanup")
}

func TestReconcileRawObjectFinalizerBlockedWithoutServiceAccount(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	sa := serviceAccountObj(rawSAName)
	r, _ := newRawTestReconciler(jt, sa)

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Delete the ServiceAccount, then the CR: finalization must fail closed
	require.NoError(t, r.Delete(context.Background(), sa))
	got := getJinjaTemplate(t, r, "gnp")
	require.NoError(t, r.Delete(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp")
	require.Error(t, err, "finalization without the ServiceAccount must retry")

	// CR still terminating (finalizer present), output untouched
	got = getJinjaTemplate(t, r, "gnp")
	assert.True(t, controllerutil.ContainsFinalizer(got, FinalizerRawOutputCleanup))
	cr := &rbacv1.ClusterRole{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr))

	// Restoring the ServiceAccount unblocks the finalizer
	require.NoError(t, r.Create(context.Background(), serviceAccountObj(rawSAName)))
	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)
	jtErr := r.Get(context.Background(), types.NamespacedName{Name: "gnp", Namespace: "infra"}, &jtov1.JinjaTemplate{})
	assert.True(t, apierrors.IsNotFound(jtErr))
}

func TestReconcileSwitchFromRawToConfigMap(t *testing.T) {
	jt := rawJinjaTemplate("gnp", clusterRoleManifest)
	r, impersonated := newRawTestReconciler(jt, serviceAccountObj(rawSAName))

	_, err := reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Switch the output to a ConfigMap (serviceAccountName must be unset)
	got := getJinjaTemplate(t, r, "gnp")
	got.Spec.Template = "hello"
	got.Spec.Output = jtov1.Output{Kind: OutputKindConfigMap}
	require.NoError(t, r.Update(context.Background(), got))

	_, err = reconcileOnce(t, r, "gnp")
	require.NoError(t, err)

	// Old raw output deleted under the identity recorded in status,
	// finalizer dropped, ConfigMap created
	cr := &rbacv1.ClusterRole{}
	getErr := r.Get(context.Background(), types.NamespacedName{Name: "raw-cluster-role"}, cr)
	assert.True(t, apierrors.IsNotFound(getErr))
	assert.Equal(t, []string{"infra/" + rawSAName, "infra/" + rawSAName}, *impersonated,
		"cleanup after the switch must impersonate the identity from status.lastOutput")

	got = getJinjaTemplate(t, r, "gnp")
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
