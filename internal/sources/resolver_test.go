package sources

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = jtov1.AddToScheme(s)
	return s
}

func TestResolveDirectConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"host": "db.example.com",
			"port": "5432",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cm).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "db_host",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "my-config",
				Key:  "host",
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", sources)
	require.NoError(t, err)
	assert.Equal(t, "db.example.com", result["db_host"])
}

func TestResolveDirectSecret(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"password": []byte("s3cret"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(secret).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "db_password",
			Secret: &jtov1.SecretSource{
				Name: "my-secret",
				Key:  "password",
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", sources)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", result["db_password"])
}

func TestResolveConfigMapByLabelSelector(t *testing.T) {
	cm1 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "endpoint-a",
			Namespace: "default",
			Labels: map[string]string{
				"type": "endpoint",
			},
		},
		Data: map[string]string{
			"url": "http://a.example.com",
		},
	}
	cm2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "endpoint-b",
			Namespace: "default",
			Labels: map[string]string{
				"type": "endpoint",
			},
		},
		Data: map[string]string{
			"url": "http://b.example.com",
		},
	}
	cm3 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-config",
			Namespace: "default",
			Labels: map[string]string{
				"type": "database",
			},
		},
		Data: map[string]string{
			"host": "db.example.com",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cm1, cm2, cm3).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
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
	}

	result, err := resolver.Resolve(context.Background(), "default", sources)
	require.NoError(t, err)

	endpoints, ok := result["endpoints"].([]map[string]interface{})
	require.True(t, ok, "endpoints should be a list of maps")
	assert.Len(t, endpoints, 2)

	// Verify the endpoints contain the expected data
	names := make(map[string]bool)
	for _, ep := range endpoints {
		names[ep["name"].(string)] = true
	}
	assert.True(t, names["endpoint-a"])
	assert.True(t, names["endpoint-b"])
}

func TestResolveSecretByLabelSelector(t *testing.T) {
	s1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-a",
			Namespace: "default",
			Labels: map[string]string{
				"type": "credential",
			},
		},
		Data: map[string][]byte{
			"token": []byte("token-a"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(s1).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "credentials",
			Secret: &jtov1.SecretSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"type": "credential",
					},
				},
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", sources)
	require.NoError(t, err)

	creds, ok := result["credentials"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, creds, 1)
	assert.Equal(t, "cred-a", creds[0]["name"])
}

func TestResolveMultipleSources(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"host": "app.example.com",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"api_key": []byte("key-123"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cm, secret).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "host",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "app-config",
				Key:  "host",
			},
		},
		{
			Name: "api_key",
			Secret: &jtov1.SecretSource{
				Name: "app-secret",
				Key:  "api_key",
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", sources)
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", result["host"])
	assert.Equal(t, "key-123", result["api_key"])
}

func TestResolveMissingConfigMap(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "missing",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "nonexistent",
				Key:  "key",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", sources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve source")
}

func TestResolveMissingKey(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"existing_key": "value",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(cm).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "missing_key",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "my-config",
				Key:  "nonexistent_key",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", sources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key \"nonexistent_key\" not found")
}

func TestResolveSourceWithNoReference(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	sources := []jtov1.Source{
		{
			Name: "bad_source",
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", sources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must specify either configMap or secret")
}

func TestResolveDirectConfigMapMissingNameOrKey(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)

	// Missing key
	sources := []jtov1.Source{
		{
			Name: "no_key",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "my-config",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", sources)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires both name and key")
}

func TestResolveDirectSecretMissingNameOrKey(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)

	// Missing key
	srcs := []jtov1.Source{
		{
			Name: "no_key",
			Secret: &jtov1.SecretSource{
				Name: "my-secret",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", srcs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires both name and key")

	// Missing name
	srcs = []jtov1.Source{
		{
			Name: "no_name",
			Secret: &jtov1.SecretSource{
				Key: "password",
			},
		},
	}

	_, err = resolver.Resolve(context.Background(), "default", srcs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires both name and key")
}

func TestResolveDirectSecretMissingKey(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"existing_key": []byte("value"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(secret).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "missing_key",
			Secret: &jtov1.SecretSource{
				Name: "my-secret",
				Key:  "nonexistent_key",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", srcs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key \"nonexistent_key\" not found")
}

func TestResolveMissingSecret(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "missing",
			Secret: &jtov1.SecretSource{
				Name: "nonexistent",
				Key:  "key",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", srcs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve source")
	assert.Contains(t, err.Error(), "failed to get Secret")
}

func TestResolveConfigMapByLabelSelectorEmpty(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "empty_result",
			ConfigMap: &jtov1.ConfigMapSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"type": "nonexistent",
					},
				},
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", srcs)
	require.NoError(t, err)

	endpoints, ok := result["empty_result"].([]map[string]interface{})
	require.True(t, ok)
	assert.Empty(t, endpoints)
}

func TestResolveSecretByLabelSelectorEmpty(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "empty_secrets",
			Secret: &jtov1.SecretSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"type": "nonexistent",
					},
				},
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", srcs)
	require.NoError(t, err)

	secrets, ok := result["empty_secrets"].([]map[string]interface{})
	require.True(t, ok)
	assert.Empty(t, secrets)
}

func TestResolveSecretByLabelSelectorMultiple(t *testing.T) {
	s1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-a",
			Namespace: "default",
			Labels:    map[string]string{"type": "credential"},
		},
		Data: map[string][]byte{
			"token": []byte("token-a"),
		},
	}
	s2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-b",
			Namespace: "default",
			Labels:    map[string]string{"type": "credential"},
		},
		Data: map[string][]byte{
			"token": []byte("token-b"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(s1, s2).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "creds",
			Secret: &jtov1.SecretSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"type": "credential"},
				},
			},
		},
	}

	result, err := resolver.Resolve(context.Background(), "default", srcs)
	require.NoError(t, err)

	creds, ok := result["creds"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, creds, 2)

	names := map[string]bool{}
	for _, c := range creds {
		names[c["name"].(string)] = true
	}
	assert.True(t, names["cred-a"])
	assert.True(t, names["cred-b"])
}

func TestResolveEmptySources(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)

	result, err := resolver.Resolve(context.Background(), "default", nil)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, result)
}

func TestGetReferencedResources(t *testing.T) {
	srcs := []jtov1.Source{
		{
			Name: "direct_cm",
			ConfigMap: &jtov1.ConfigMapSource{
				Name: "my-config",
				Key:  "host",
			},
		},
		{
			Name: "direct_secret",
			Secret: &jtov1.SecretSource{
				Name: "my-secret",
				Key:  "password",
			},
		},
		{
			Name: "label_cm",
			ConfigMap: &jtov1.ConfigMapSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"type": "endpoint"},
				},
			},
		},
		{
			Name: "label_secret",
			Secret: &jtov1.SecretSource{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"type": "credential"},
				},
			},
		},
	}

	cmNames, secretNames, cmSelectors, secretSelectors := GetReferencedResources(srcs)

	assert.Equal(t, []string{"my-config"}, cmNames)
	assert.Equal(t, []string{"my-secret"}, secretNames)
	require.Len(t, cmSelectors, 1)
	require.Len(t, secretSelectors, 1)
}

func TestGetReferencedResourcesEmpty(t *testing.T) {
	cmNames, secretNames, cmSelectors, secretSelectors := GetReferencedResources(nil)

	assert.Empty(t, cmNames)
	assert.Empty(t, secretNames)
	assert.Empty(t, cmSelectors)
	assert.Empty(t, secretSelectors)
}

func TestGetReferencedResourcesDirectOnly(t *testing.T) {
	srcs := []jtov1.Source{
		{
			Name:      "cm1",
			ConfigMap: &jtov1.ConfigMapSource{Name: "config-a", Key: "k"},
		},
		{
			Name:      "cm2",
			ConfigMap: &jtov1.ConfigMapSource{Name: "config-b", Key: "k"},
		},
	}

	cmNames, secretNames, cmSelectors, secretSelectors := GetReferencedResources(srcs)

	assert.Equal(t, []string{"config-a", "config-b"}, cmNames)
	assert.Empty(t, secretNames)
	assert.Empty(t, cmSelectors)
	assert.Empty(t, secretSelectors)
}

func TestResolveDirectConfigMapMissingName(t *testing.T) {
	client := fake.NewClientBuilder().
		WithScheme(newScheme()).
		Build()

	resolver := NewResolver(client)
	srcs := []jtov1.Source{
		{
			Name: "no_name",
			ConfigMap: &jtov1.ConfigMapSource{
				Key: "some-key",
			},
		},
	}

	_, err := resolver.Resolve(context.Background(), "default", srcs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires both name and key")
}
