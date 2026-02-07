package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
	"github.com/guided-traffic/jinja-template-operator/internal/config"
	"github.com/guided-traffic/jinja-template-operator/internal/sources"
	tmpl "github.com/guided-traffic/jinja-template-operator/internal/template"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = jtov1.AddToScheme(s)
	return s
}

func newTestReconciler(objs ...runtime.Object) (*JinjaTemplateReconciler, *events.FakeRecorder) {
	scheme := newTestScheme()
	clientObjs := make([]runtime.Object, len(objs))
	copy(clientObjs, objs)

	// Build client with status subresource for JinjaTemplate
	builder := fake.NewClientBuilder().WithScheme(scheme)

	// Separate client.Objects from runtime.Objects
	for _, obj := range objs {
		if co, ok := obj.(metav1.Object); ok {
			_ = co // Ensure it's a valid object
		}
	}

	c := builder.WithRuntimeObjects(clientObjs...).
		WithStatusSubresource(&jtov1.JinjaTemplate{}).
		Build()

	recorder := events.NewFakeRecorder(10)

	reconciler := &JinjaTemplateReconciler{
		Client:   c,
		Scheme:   scheme,
		Config:   config.NewOperatorConfig(),
		Recorder: recorder,
		Renderer: tmpl.NewRenderer(),
		Resolver: sources.NewResolver(c),
	}

	return reconciler, recorder
}

func TestReconcileSimpleInlineTemplate(t *testing.T) {
	// Create source ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"host": "db.example.com",
		},
	}

	// Create JinjaTemplate CR
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "db_host",
					ConfigMap: &jtov1.ConfigMapSource{
						Name: "db-config",
						Key:  "host",
					},
				},
			},
			Template: "DATABASE_HOST={{ db_host }}",
			Output: jtov1.Output{
				Kind: "ConfigMap",
			},
		},
	}

	reconciler, _ := newTestReconciler(cm, jt)

	// Reconcile
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "app-config",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify output ConfigMap was created
	outputCM := &corev1.ConfigMap{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "app-config",
		Namespace: "default",
	}, outputCM)
	// The output ConfigMap name defaults to the CR name, which conflicts with the CR itself
	// Actually, ConfigMaps and JinjaTemplates are different resource types, so the name can be the same
	// But the source ConfigMap "db-config" is different from the output "app-config"
	require.NoError(t, err)
	assert.Contains(t, outputCM.Data["content"], "DATABASE_HOST=db.example.com")
	assert.Equal(t, ManagerName, outputCM.Labels[LabelManagedBy])
}

func TestReconcileSecretOutput(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-creds",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"password": []byte("s3cret"),
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "generated-secret",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "password",
					Secret: &jtov1.SecretSource{
						Name: "db-creds",
						Key:  "password",
					},
				},
			},
			Template: "DB_PASSWORD={{ password }}",
			Output: jtov1.Output{
				Kind: "Secret",
				Name: "app-secret",
			},
		},
	}

	reconciler, _ := newTestReconciler(secret, jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "generated-secret",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify output Secret was created
	outputSecret := &corev1.Secret{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "app-secret",
		Namespace: "default",
	}, outputSecret)
	require.NoError(t, err)
	assert.Equal(t, "DB_PASSWORD=s3cret", string(outputSecret.Data["content"]))
}

func TestReconcileNotFound(t *testing.T) {
	reconciler, _ := newTestReconciler()

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcileInvalidSpec(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-template",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			// No template specified
			Output: jtov1.Output{
				Kind: "ConfigMap",
			},
		},
	}

	reconciler, recorder := newTestReconciler(jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "bad-template",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Check that an event was recorded
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, ReasonInvalidSpec)
	default:
		t.Error("Expected an event to be recorded")
	}
}

func TestValidateSpec(t *testing.T) {
	reconciler, _ := newTestReconciler()

	tests := []struct {
		name    string
		jt      *jtov1.JinjaTemplate
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid inline template",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Template: "Hello {{ name }}",
					Output:   jtov1.Output{Kind: "ConfigMap"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid external template",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					TemplateFrom: &jtov1.TemplateFrom{
						ConfigMapRef: &jtov1.ConfigMapKeyRef{
							Name: "tpl-cm",
							Key:  "template",
						},
					},
					Output: jtov1.Output{Kind: "Secret"},
				},
			},
			wantErr: false,
		},
		{
			name: "no template",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Output: jtov1.Output{Kind: "ConfigMap"},
				},
			},
			wantErr: true,
			errMsg:  "either spec.template or spec.templateFrom.configMapRef must be provided",
		},
		{
			name: "both template and templateFrom",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Template: "Hello",
					TemplateFrom: &jtov1.TemplateFrom{
						ConfigMapRef: &jtov1.ConfigMapKeyRef{
							Name: "tpl-cm",
							Key:  "template",
						},
					},
					Output: jtov1.Output{Kind: "ConfigMap"},
				},
			},
			wantErr: true,
			errMsg:  "only one of",
		},
		{
			name: "invalid output kind",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Template: "Hello",
					Output:   jtov1.Output{Kind: "Deployment"},
				},
			},
			wantErr: true,
			errMsg:  "must be either 'ConfigMap' or 'Secret'",
		},
		{
			name: "source without configMap or secret",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Template: "Hello",
					Output:   jtov1.Output{Kind: "ConfigMap"},
					Sources: []jtov1.Source{
						{Name: "bad"},
					},
				},
			},
			wantErr: true,
			errMsg:  "must specify either configMap or secret",
		},
		{
			name: "source with both configMap and secret",
			jt: &jtov1.JinjaTemplate{
				Spec: jtov1.JinjaTemplateSpec{
					Template: "Hello",
					Output:   jtov1.Output{Kind: "ConfigMap"},
					Sources: []jtov1.Source{
						{
							Name:      "both",
							ConfigMap: &jtov1.ConfigMapSource{Name: "cm", Key: "k"},
							Secret:    &jtov1.SecretSource{Name: "s", Key: "k"},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "not both",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.validateSpec(tt.jt)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSetCondition(t *testing.T) {
	reconciler, _ := newTestReconciler()
	jt := &jtov1.JinjaTemplate{}

	// Set initial condition
	reconciler.setCondition(jt, metav1.ConditionTrue, ReasonRenderSuccess, "success")
	require.Len(t, jt.Status.Conditions, 1)
	assert.Equal(t, ConditionReady, jt.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, jt.Status.Conditions[0].Status)

	// Update condition
	reconciler.setCondition(jt, metav1.ConditionFalse, ReasonRenderFailed, "error")
	require.Len(t, jt.Status.Conditions, 1) // Still only one condition
	assert.Equal(t, metav1.ConditionFalse, jt.Status.Conditions[0].Status)
	assert.Equal(t, ReasonRenderFailed, jt.Status.Conditions[0].Reason)
}

func TestSetConditionUnchanged(t *testing.T) {
	reconciler, _ := newTestReconciler()
	jt := &jtov1.JinjaTemplate{}

	// Set initial condition
	reconciler.setCondition(jt, metav1.ConditionTrue, ReasonRenderSuccess, "success")
	ts := jt.Status.Conditions[0].LastTransitionTime

	// Set same condition again — should not update the timestamp
	reconciler.setCondition(jt, metav1.ConditionTrue, ReasonRenderSuccess, "success")
	require.Len(t, jt.Status.Conditions, 1)
	assert.Equal(t, ts, jt.Status.Conditions[0].LastTransitionTime)
}

func TestLoadTemplateInline(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Template: "Hello {{ name }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)
	tpl, err := reconciler.loadTemplate(context.Background(), jt)
	require.NoError(t, err)
	assert.Equal(t, "Hello {{ name }}", tpl)
}

func TestLoadTemplateFromConfigMap(t *testing.T) {
	templateCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tpl-configmap",
			Namespace: "default",
		},
		Data: map[string]string{
			"template.j2": "DB_HOST={{ host }}",
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "tpl-configmap",
					Key:  "template.j2",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(templateCM, jt)
	tpl, err := reconciler.loadTemplate(context.Background(), jt)
	require.NoError(t, err)
	assert.Equal(t, "DB_HOST={{ host }}", tpl)
}

func TestLoadTemplateFromConfigMapNotFound(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "nonexistent-cm",
					Key:  "template",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)
	_, err := reconciler.loadTemplate(context.Background(), jt)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get template ConfigMap")
}

func TestLoadTemplateFromConfigMapKeyNotFound(t *testing.T) {
	templateCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tpl-configmap",
			Namespace: "default",
		},
		Data: map[string]string{
			"other-key": "some data",
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "tpl-configmap",
					Key:  "missing-key",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(templateCM, jt)
	_, err := reconciler.loadTemplate(context.Background(), jt)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key \"missing-key\" not found")
}

func TestLoadTemplateNoSource(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			// No template and no templateFrom
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)
	_, err := reconciler.loadTemplate(context.Background(), jt)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no template source configured")
}

func TestReconcileSourceResolutionFailed(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failing-source",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "missing_cm",
					ConfigMap: &jtov1.ConfigMapSource{
						Name: "nonexistent",
						Key:  "key",
					},
				},
			},
			Template: "VALUE={{ missing_cm }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, recorder := newTestReconciler(jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "failing-source", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "source resolution failed")
	assert.Equal(t, ctrl.Result{}, result)

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, ReasonSourceResolutionFailed)
	default:
		t.Error("Expected SourceResolutionFailed event")
	}
}

func TestReconcileRenderFailed(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "src-cm",
			Namespace: "default",
		},
		Data: map[string]string{
			"val": "hello",
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-render",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "val",
					ConfigMap: &jtov1.ConfigMapSource{
						Name: "src-cm",
						Key:  "val",
					},
				},
			},
			Template: "{% invalid_tag %}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, recorder := newTestReconciler(cm, jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-render", Namespace: "default"},
	})

	// Render errors don't requeue (no error returned)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, ReasonRenderFailed)
	default:
		t.Error("Expected RenderFailed event")
	}
}

func TestReconcileTemplateLoadFailed(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tpl-load-fail",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "nonexistent-cm",
					Key:  "template",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, recorder := newTestReconciler(jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tpl-load-fail", Namespace: "default"},
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "template load failed")
	assert.Equal(t, ctrl.Result{}, result)

	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, ReasonTemplateLoadFailed)
	default:
		t.Error("Expected TemplateLoadFailed event")
	}
}

func TestReconcileWithCustomOutputName(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "src",
			Namespace: "default",
		},
		Data: map[string]string{
			"key": "value",
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "val",
					ConfigMap: &jtov1.ConfigMapSource{Name: "src", Key: "key"},
				},
			},
			Template: "result={{ val }}",
			Output: jtov1.Output{
				Kind: "ConfigMap",
				Name: "custom-output-name",
			},
		},
	}

	reconciler, _ := newTestReconciler(cm, jt)

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-jt", Namespace: "default"},
	})
	require.NoError(t, err)

	// Verify output ConfigMap uses the custom name
	outputCM := &corev1.ConfigMap{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name:      "custom-output-name",
		Namespace: "default",
	}, outputCM)
	require.NoError(t, err)
	assert.Equal(t, "result=value", outputCM.Data["content"])
	assert.Equal(t, "my-jt", outputCM.Labels[LabelJinjaTemplate])
}

func TestReconcileOwnerReferenceDisabled(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "src",
			Namespace: "default",
		},
		Data: map[string]string{
			"k": "v",
		},
	}

	ownerRefFalse := false
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-owner",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			SetOwnerReference: &ownerRefFalse,
			Sources: []jtov1.Source{
				{
					Name:      "k",
					ConfigMap: &jtov1.ConfigMapSource{Name: "src", Key: "k"},
				},
			},
			Template: "KEY={{ k }}",
			Output:   jtov1.Output{Kind: "ConfigMap", Name: "output-no-owner"},
		},
	}

	reconciler, _ := newTestReconciler(cm, jt)
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-owner", Namespace: "default"},
	})
	require.NoError(t, err)

	outputCM := &corev1.ConfigMap{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name: "output-no-owner", Namespace: "default",
	}, outputCM)
	require.NoError(t, err)
	assert.Equal(t, "KEY=v", outputCM.Data["content"])
	// OwnerReferences should be empty when disabled
	assert.Empty(t, outputCM.OwnerReferences)
}

func TestReconcileSecretOutputWithOwnerRef(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "src-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"token": []byte("abc123"),
		},
	}

	ownerRefTrue := true
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret-owner",
			Namespace: "default",
			UID:       "test-uid-123",
		},
		Spec: jtov1.JinjaTemplateSpec{
			SetOwnerReference: &ownerRefTrue,
			Sources: []jtov1.Source{
				{
					Name:   "token",
					Secret: &jtov1.SecretSource{Name: "src-secret", Key: "token"},
				},
			},
			Template: "TOKEN={{ token }}",
			Output:   jtov1.Output{Kind: "Secret", Name: "output-secret"},
		},
	}

	reconciler, _ := newTestReconciler(secret, jt)
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "secret-owner", Namespace: "default"},
	})
	require.NoError(t, err)

	outputSecret := &corev1.Secret{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name: "output-secret", Namespace: "default",
	}, outputSecret)
	require.NoError(t, err)
	assert.Equal(t, "TOKEN=abc123", string(outputSecret.Data["content"]))
	assert.Equal(t, corev1.SecretTypeOpaque, outputSecret.Type)
}

func TestValidateSpecSourceEmptyName(t *testing.T) {
	reconciler, _ := newTestReconciler()
	jt := &jtov1.JinjaTemplate{
		Spec: jtov1.JinjaTemplateSpec{
			Template: "Hello",
			Output:   jtov1.Output{Kind: "ConfigMap"},
			Sources: []jtov1.Source{
				{
					Name:      "",
					ConfigMap: &jtov1.ConfigMapSource{Name: "cm", Key: "k"},
				},
			},
		},
	}

	err := reconciler.validateSpec(jt)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "source name must not be empty")
}

func TestMergeLabels(t *testing.T) {
	tests := []struct {
		name       string
		existing   map[string]string
		additional map[string]string
		expected   map[string]string
	}{
		{
			name:       "nil existing",
			existing:   nil,
			additional: map[string]string{"a": "1"},
			expected:   map[string]string{"a": "1"},
		},
		{
			name:       "empty existing",
			existing:   map[string]string{},
			additional: map[string]string{"a": "1"},
			expected:   map[string]string{"a": "1"},
		},
		{
			name:       "merge with existing",
			existing:   map[string]string{"existing": "label"},
			additional: map[string]string{"new": "label"},
			expected:   map[string]string{"existing": "label", "new": "label"},
		},
		{
			name:       "overwrite managed labels",
			existing:   map[string]string{"a": "old"},
			additional: map[string]string{"a": "new"},
			expected:   map[string]string{"a": "new"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeLabels(tt.existing, tt.additional)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindJinjaTemplatesForConfigMap(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "watcher-jt",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "db_host",
					ConfigMap: &jtov1.ConfigMapSource{
						Name: "db-config",
						Key:  "host",
					},
				},
			},
			Template: "HOST={{ db_host }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	// Trigger with a ConfigMap that matches a source
	matchingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-config",
			Namespace: "default",
		},
	}

	requests := reconciler.findJinjaTemplatesForConfigMap(context.Background(), matchingCM)
	require.Len(t, requests, 1)
	assert.Equal(t, "watcher-jt", requests[0].Name)
	assert.Equal(t, "default", requests[0].Namespace)

	// Trigger with a ConfigMap that does NOT match any source
	unrelatedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-cm",
			Namespace: "default",
		},
	}

	requests = reconciler.findJinjaTemplatesForConfigMap(context.Background(), unrelatedCM)
	assert.Empty(t, requests)
}

func TestFindJinjaTemplatesForSecret(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret-watcher",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "password",
					Secret: &jtov1.SecretSource{
						Name: "db-creds",
						Key:  "password",
					},
				},
			},
			Template: "PASS={{ password }}",
			Output:   jtov1.Output{Kind: "Secret"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	matchingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-creds",
			Namespace: "default",
		},
	}

	requests := reconciler.findJinjaTemplatesForSecret(context.Background(), matchingSecret)
	require.Len(t, requests, 1)
	assert.Equal(t, "secret-watcher", requests[0].Name)

	unrelatedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-secret",
			Namespace: "default",
		},
	}

	requests = reconciler.findJinjaTemplatesForSecret(context.Background(), unrelatedSecret)
	assert.Empty(t, requests)
}

func TestFindJinjaTemplatesForConfigMapWithLabelSelector(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "label-watcher",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "endpoints",
					ConfigMap: &jtov1.ConfigMapSource{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"type": "endpoint",
							},
						},
					},
				},
			},
			Template: "{% for ep in endpoints %}{{ ep.name }}{% endfor %}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	// ConfigMap matching the label selector
	matchingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "endpoint-a",
			Namespace: "default",
			Labels: map[string]string{
				"type": "endpoint",
			},
		},
	}

	requests := reconciler.findJinjaTemplatesForConfigMap(context.Background(), matchingCM)
	require.Len(t, requests, 1)
	assert.Equal(t, "label-watcher", requests[0].Name)

	// ConfigMap NOT matching the label selector
	nonMatchingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-cm",
			Namespace: "default",
			Labels: map[string]string{
				"type": "database",
			},
		},
	}

	requests = reconciler.findJinjaTemplatesForConfigMap(context.Background(), nonMatchingCM)
	assert.Empty(t, requests)
}

func TestFindJinjaTemplatesForTemplateFromConfigMap(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "templatefrom-watcher",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "template-cm",
					Key:  "tpl",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	// ConfigMap that is the template source
	templateCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template-cm",
			Namespace: "default",
		},
	}

	requests := reconciler.findJinjaTemplatesForConfigMap(context.Background(), templateCM)
	require.Len(t, requests, 1)
	assert.Equal(t, "templatefrom-watcher", requests[0].Name)
}

func TestSourceMatchesObject(t *testing.T) {
	reconciler, _ := newTestReconciler()

	tests := []struct {
		name       string
		directName string
		selector   *metav1.LabelSelector
		obj        *corev1.ConfigMap
		expected   bool
	}{
		{
			name:       "direct name match",
			directName: "my-config",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "my-config"},
			},
			expected: true,
		},
		{
			name:       "direct name no match",
			directName: "my-config",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "other-config"},
			},
			expected: false,
		},
		{
			name: "label selector match",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "web-config",
					Labels: map[string]string{"app": "web"},
				},
			},
			expected: true,
		},
		{
			name: "label selector no match",
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
			},
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "db-config",
					Labels: map[string]string{"app": "db"},
				},
			},
			expected: false,
		},
		{
			name:       "no direct name and no selector",
			directName: "",
			selector:   nil,
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "anything"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconciler.sourceMatchesObject(tt.directName, tt.selector, tt.obj)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestObjectReferencedByJinjaTemplate(t *testing.T) {
	reconciler, _ := newTestReconciler()

	jt := &jtov1.JinjaTemplate{
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "db_host",
					ConfigMap: &jtov1.ConfigMapSource{Name: "db-config", Key: "host"},
				},
				{
					Name:   "password",
					Secret: &jtov1.SecretSource{Name: "db-creds", Key: "password"},
				},
			},
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{Name: "template-cm", Key: "tpl"},
			},
			Output: jtov1.Output{Kind: "ConfigMap"},
		},
	}

	// ConfigMap referenced as source
	assert.True(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db-config"}}, OutputKindConfigMap))

	// ConfigMap referenced via templateFrom
	assert.True(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "template-cm"}}, OutputKindConfigMap))

	// Secret referenced as source
	assert.True(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-creds"}}, OutputKindSecret))

	// Unrelated ConfigMap
	assert.False(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}, OutputKindConfigMap))

	// Unrelated Secret
	assert.False(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}}, OutputKindSecret))

	// ConfigMap kind check against Secret source should not match
	assert.False(t, reconciler.objectReferencedByJinjaTemplate(jt,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db-creds"}}, OutputKindConfigMap))
}

func TestFindJinjaTemplatesForSecretWithLabelSelector(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "secret-label-watcher",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name: "creds",
					Secret: &jtov1.SecretSource{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"type": "credential",
							},
						},
					},
				},
			},
			Template: "{% for c in creds %}{{ c.name }}{% endfor %}",
			Output:   jtov1.Output{Kind: "Secret"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	matchingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-a",
			Namespace: "default",
			Labels:    map[string]string{"type": "credential"},
		},
	}

	requests := reconciler.findJinjaTemplatesForSecret(context.Background(), matchingSecret)
	require.Len(t, requests, 1)
	assert.Equal(t, "secret-label-watcher", requests[0].Name)

	nonMatchingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other",
			Namespace: "default",
			Labels:    map[string]string{"type": "other"},
		},
	}

	requests = reconciler.findJinjaTemplatesForSecret(context.Background(), nonMatchingSecret)
	assert.Empty(t, requests)
}

func TestReconcileExternalTemplateEndToEnd(t *testing.T) {
	templateCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-tpl",
			Namespace: "default",
		},
		Data: map[string]string{
			"template.j2": "GREETING={{ greeting }}",
		},
	}

	srcCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "src-data",
			Namespace: "default",
		},
		Data: map[string]string{
			"msg": "Hello",
		},
	}

	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-test",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "greeting",
					ConfigMap: &jtov1.ConfigMapSource{Name: "src-data", Key: "msg"},
				},
			},
			TemplateFrom: &jtov1.TemplateFrom{
				ConfigMapRef: &jtov1.ConfigMapKeyRef{
					Name: "external-tpl",
					Key:  "template.j2",
				},
			},
			Output: jtov1.Output{Kind: "ConfigMap", Name: "external-output"},
		},
	}

	reconciler, _ := newTestReconciler(templateCM, srcCM, jt)

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "external-test", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	outputCM := &corev1.ConfigMap{}
	err = reconciler.Get(context.Background(), types.NamespacedName{
		Name: "external-output", Namespace: "default",
	}, outputCM)
	require.NoError(t, err)
	assert.Equal(t, "GREETING=Hello", outputCM.Data["content"])
}

func TestReconcileMultipleJinjaTemplatesWatching(t *testing.T) {
	jt1 := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "jt-one",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "val",
					ConfigMap: &jtov1.ConfigMapSource{Name: "shared-cm", Key: "k"},
				},
			},
			Template: "A={{ val }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	jt2 := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "jt-two",
			Namespace: "default",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "val",
					ConfigMap: &jtov1.ConfigMapSource{Name: "shared-cm", Key: "k"},
				},
			},
			Template: "B={{ val }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt1, jt2)

	sharedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-cm",
			Namespace: "default",
		},
	}

	// Both JTs should be returned when the shared ConfigMap changes
	requests := reconciler.findJinjaTemplatesForConfigMap(context.Background(), sharedCM)
	assert.Len(t, requests, 2)

	names := map[string]bool{}
	for _, r := range requests {
		names[r.Name] = true
	}
	assert.True(t, names["jt-one"])
	assert.True(t, names["jt-two"])
}

func TestFindJinjaTemplatesDifferentNamespace(t *testing.T) {
	jt := &jtov1.JinjaTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ns-jt",
			Namespace: "production",
		},
		Spec: jtov1.JinjaTemplateSpec{
			Sources: []jtov1.Source{
				{
					Name:      "val",
					ConfigMap: &jtov1.ConfigMapSource{Name: "my-cm", Key: "k"},
				},
			},
			Template: "V={{ val }}",
			Output:   jtov1.Output{Kind: "ConfigMap"},
		},
	}

	reconciler, _ := newTestReconciler(jt)

	// ConfigMap in a different namespace should not match
	otherNsCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cm",
			Namespace: "staging",
		},
	}

	requests := reconciler.findJinjaTemplatesForConfigMap(context.Background(), otherNsCM)
	assert.Empty(t, requests)
}
