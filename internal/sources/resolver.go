// Package sources provides resolution of variable sources for JinjaTemplate CRs.
package sources

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	jtov1 "github.com/guided-traffic/jinja-template-operator/api/v1"
)

// Resolver resolves JinjaTemplate sources into a template context map.
type Resolver struct {
	Client client.Client
}

// NewResolver creates a new source Resolver.
func NewResolver(c client.Client) *Resolver {
	return &Resolver{Client: c}
}

// Resolve resolves all sources for a JinjaTemplate into a context map.
// The returned map can be passed directly to the template renderer.
func (r *Resolver) Resolve(ctx context.Context, namespace string, sources []jtov1.Source) (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(sources))

	for _, src := range sources {
		value, err := r.resolveSource(ctx, namespace, src)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve source %q: %w", src.Name, err)
		}
		result[src.Name] = value
	}

	return result, nil
}

// resolveSource resolves a single source to its value.
func (r *Resolver) resolveSource(ctx context.Context, namespace string, src jtov1.Source) (interface{}, error) {
	switch {
	case src.ConfigMap != nil:
		return r.resolveConfigMapSource(ctx, namespace, src.ConfigMap)
	case src.Secret != nil:
		return r.resolveSecretSource(ctx, namespace, src.Secret)
	default:
		return nil, fmt.Errorf("source %q must specify either configMap or secret", src.Name)
	}
}

// resolveConfigMapSource resolves a ConfigMap source, either direct or via label selector.
func (r *Resolver) resolveConfigMapSource(ctx context.Context, namespace string, src *jtov1.ConfigMapSource) (interface{}, error) {
	if src.LabelSelector != nil {
		return r.resolveConfigMapByLabel(ctx, namespace, src.LabelSelector)
	}
	return r.resolveConfigMapDirect(ctx, namespace, src.Name, src.Key)
}

// resolveSecretSource resolves a Secret source, either direct or via label selector.
func (r *Resolver) resolveSecretSource(ctx context.Context, namespace string, src *jtov1.SecretSource) (interface{}, error) {
	if src.LabelSelector != nil {
		return r.resolveSecretByLabel(ctx, namespace, src.LabelSelector)
	}
	return r.resolveSecretDirect(ctx, namespace, src.Name, src.Key)
}

// resolveConfigMapDirect resolves a direct ConfigMap reference (name + key) to a single string value.
func (r *Resolver) resolveConfigMapDirect(ctx context.Context, namespace, name, key string) (string, error) {
	if name == "" || key == "" {
		return "", fmt.Errorf("direct ConfigMap reference requires both name and key")
	}

	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cm); err != nil {
		return "", fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, name, err)
	}

	value, ok := cm.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in ConfigMap %s/%s", key, namespace, name)
	}

	return value, nil
}

// resolveSecretDirect resolves a direct Secret reference (name + key) to a single string value.
func (r *Resolver) resolveSecretDirect(ctx context.Context, namespace, name, key string) (string, error) {
	if name == "" || key == "" {
		return "", fmt.Errorf("direct Secret reference requires both name and key")
	}

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return "", fmt.Errorf("failed to get Secret %s/%s: %w", namespace, name, err)
	}

	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in Secret %s/%s", key, namespace, name)
	}

	return string(value), nil
}

// LabelSelectorObject represents a resource matched by a label selector.
// It is exposed as a list element in the template context.
type LabelSelectorObject struct {
	Name string            `json:"name"`
	Data map[string]string `json:"data"`
}

// resolveConfigMapByLabel resolves ConfigMaps matching a label selector into a list of objects.
func (r *Resolver) resolveConfigMapByLabel(ctx context.Context, namespace string, selector *metav1.LabelSelector) ([]map[string]interface{}, error) {
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	cmList := &corev1.ConfigMapList{}
	if err := r.Client.List(ctx, cmList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: sel},
	); err != nil {
		return nil, fmt.Errorf("failed to list ConfigMaps with selector %v: %w", selector, err)
	}

	return configMapsToObjects(cmList.Items), nil
}

// resolveSecretByLabel resolves Secrets matching a label selector into a list of objects.
func (r *Resolver) resolveSecretByLabel(ctx context.Context, namespace string, selector *metav1.LabelSelector) ([]map[string]interface{}, error) {
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector: %w", err)
	}

	secretList := &corev1.SecretList{}
	if err := r.Client.List(ctx, secretList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: sel},
	); err != nil {
		return nil, fmt.Errorf("failed to list Secrets with selector %v: %w", selector, err)
	}

	return secretsToObjects(secretList.Items), nil
}

// configMapsToObjects converts a list of ConfigMaps to template-friendly objects.
func configMapsToObjects(cms []corev1.ConfigMap) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(cms))
	for _, cm := range cms {
		data := make(map[string]interface{}, len(cm.Data))
		for k, v := range cm.Data {
			data[k] = v
		}
		result = append(result, map[string]interface{}{
			"name": cm.Name,
			"data": data,
		})
	}
	return result
}

// secretsToObjects converts a list of Secrets to template-friendly objects.
func secretsToObjects(secrets []corev1.Secret) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(secrets))
	for _, s := range secrets {
		data := make(map[string]interface{}, len(s.Data))
		for k, v := range s.Data {
			data[k] = string(v)
		}
		result = append(result, map[string]interface{}{
			"name": s.Name,
			"data": data,
		})
	}
	return result
}

// GetReferencedResources returns the names and types of all resources
// directly referenced by the given sources. Used for watch mapping.
func GetReferencedResources(sources []jtov1.Source) (configMapNames, secretNames []string, configMapSelectors, secretSelectors []labels.Selector) {
	for _, src := range sources {
		if src.ConfigMap != nil {
			if src.ConfigMap.LabelSelector != nil {
				sel, err := metav1.LabelSelectorAsSelector(src.ConfigMap.LabelSelector)
				if err == nil {
					configMapSelectors = append(configMapSelectors, sel)
				}
			} else if src.ConfigMap.Name != "" {
				configMapNames = append(configMapNames, src.ConfigMap.Name)
			}
		}
		if src.Secret != nil {
			if src.Secret.LabelSelector != nil {
				sel, err := metav1.LabelSelectorAsSelector(src.Secret.LabelSelector)
				if err == nil {
					secretSelectors = append(secretSelectors, sel)
				}
			} else if src.Secret.Name != "" {
				secretNames = append(secretNames, src.Secret.Name)
			}
		}
	}
	return
}
