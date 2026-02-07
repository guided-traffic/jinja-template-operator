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
	"k8s.io/client-go/tools/record"
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

func newTestReconciler(objs ...runtime.Object) (*JinjaTemplateReconciler, *record.FakeRecorder) {
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

	recorder := record.NewFakeRecorder(10)

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
	assert.False(t, result.Requeue)

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
	assert.False(t, result.Requeue)

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
	assert.False(t, result.Requeue)
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
	assert.False(t, result.Requeue)

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
