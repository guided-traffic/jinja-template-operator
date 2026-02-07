package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// JinjaTemplateSpec defines the desired state of a JinjaTemplate.
type JinjaTemplateSpec struct {
	// Sources defines the variable sources for the Jinja template context.
	// +optional
	Sources []Source `json:"sources,omitempty"`

	// Template is an inline Jinja template string.
	// Exactly one of Template or TemplateFrom must be provided.
	// +optional
	Template string `json:"template,omitempty"`

	// TemplateFrom references an external template stored in a ConfigMap.
	// Exactly one of Template or TemplateFrom must be provided.
	// +optional
	TemplateFrom *TemplateFrom `json:"templateFrom,omitempty"`

	// Output defines the target resource to create from the rendered template.
	Output Output `json:"output"`

	// SetOwnerReference controls whether the generated resource has an OwnerReference
	// pointing to the JinjaTemplate CR. If omitted, the global default is used.
	// +optional
	SetOwnerReference *bool `json:"setOwnerReference,omitempty"`
}

// Source defines a single variable source for the template context.
// Each source has a unique name that becomes the variable name in the template.
// Either ConfigMap or Secret must be specified (never both).
type Source struct {
	// Name is the variable name used in the template.
	Name string `json:"name"`

	// ConfigMap references a ConfigMap as the source.
	// +optional
	ConfigMap *ConfigMapSource `json:"configMap,omitempty"`

	// Secret references a Secret as the source.
	// +optional
	Secret *SecretSource `json:"secret,omitempty"`
}

// ConfigMapSource defines how to resolve a ConfigMap source.
// Either a direct reference (Name + Key) or a LabelSelector must be provided.
type ConfigMapSource struct {
	// Name is the name of the ConfigMap (for direct reference).
	// +optional
	Name string `json:"name,omitempty"`

	// Key is the key within the ConfigMap (for direct reference).
	// +optional
	Key string `json:"key,omitempty"`

	// LabelSelector selects ConfigMaps by labels, resolving to a list of objects.
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// SecretSource defines how to resolve a Secret source.
// Either a direct reference (Name + Key) or a LabelSelector must be provided.
type SecretSource struct {
	// Name is the name of the Secret (for direct reference).
	// +optional
	Name string `json:"name,omitempty"`

	// Key is the key within the Secret (for direct reference).
	// +optional
	Key string `json:"key,omitempty"`

	// LabelSelector selects Secrets by labels, resolving to a list of objects.
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// TemplateFrom references a template stored externally in a ConfigMap.
type TemplateFrom struct {
	// ConfigMapRef references a ConfigMap containing the template.
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// ConfigMapKeyRef identifies a specific key in a specific ConfigMap.
type ConfigMapKeyRef struct {
	// Name is the name of the ConfigMap.
	Name string `json:"name"`

	// Key is the key in the ConfigMap containing the template.
	Key string `json:"key"`
}

// Output defines the target resource for the rendered template.
type Output struct {
	// Kind is the kind of the output resource: "ConfigMap" or "Secret".
	// +kubebuilder:validation:Enum=ConfigMap;Secret
	Kind string `json:"kind"`

	// Name is the name of the output resource.
	// Defaults to the JinjaTemplate CR name if omitted.
	// +optional
	Name string `json:"name,omitempty"`
}

// JinjaTemplateStatus defines the observed state of a JinjaTemplate.
type JinjaTemplateStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Output Kind",type=string,JSONPath=`.spec.output.kind`
// +kubebuilder:printcolumn:name="Output Name",type=string,JSONPath=`.spec.output.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// JinjaTemplate is the Schema for the jinjaTemplates API.
type JinjaTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   JinjaTemplateSpec   `json:"spec,omitempty"`
	Status JinjaTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// JinjaTemplateList contains a list of JinjaTemplate resources.
type JinjaTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []JinjaTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&JinjaTemplate{}, &JinjaTemplateList{})
}
