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
// Exactly one of ConfigMap, Secret or DNS must be specified.
type Source struct {
	// Name is the variable name used in the template.
	Name string `json:"name"`

	// ConfigMap references a ConfigMap as the source.
	// +optional
	ConfigMap *ConfigMapSource `json:"configMap,omitempty"`

	// Secret references a Secret as the source.
	// +optional
	Secret *SecretSource `json:"secret,omitempty"`

	// DNS resolves DNS records as the source.
	// +optional
	DNS *DNSSource `json:"dns,omitempty"`
}

// DNSSource defines a DNS lookup as a variable source.
// The lookup result is always a sorted list of IP address strings.
// CNAME chains are followed recursively (max 10 hops) until IP records are reached.
type DNSSource struct {
	// Host is the DNS name to resolve.
	Host string `json:"host"`

	// RecordType is the address record type to query: "A", "AAAA" or "A+AAAA"
	// (both families combined). CNAME chains are followed transparently; the
	// result contains only IP addresses.
	// +kubebuilder:validation:Enum=A;AAAA;A+AAAA
	// +kubebuilder:default=A
	// +optional
	RecordType string `json:"recordType,omitempty"`

	// RefreshIntervalSeconds forces re-resolution after a fixed interval.
	// If omitted, the record's TTL drives the refresh.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RefreshIntervalSeconds *int32 `json:"refreshIntervalSeconds,omitempty"`

	// Nameserver is an optional DNS server ("host" or "host:port", port
	// defaults to 53), also used for every hop of a CNAME chain. If omitted,
	// the system default resolver is used.
	// +optional
	Nameserver string `json:"nameserver,omitempty"`

	// RemovalGracePeriodSeconds keeps a record in the rendered list for this
	// long after it stops appearing in successful lookup responses (including
	// NXDOMAIN, which counts as an empty response). 0 or omitted means
	// immediate removal. Failed lookups (timeout, SERVFAIL) do not age
	// records; the last known state stays valid.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RemovalGracePeriodSeconds *int32 `json:"removalGracePeriodSeconds,omitempty"`
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
	// Kind is the kind of the output resource: "ConfigMap", "Secret" or
	// "RawObject". With "RawObject" the rendered template must be a complete
	// Kubernetes manifest (single YAML document including apiVersion, kind and
	// metadata.name); Name, Key and Keys must not be set and
	// ServiceAccountName is required.
	// +kubebuilder:validation:Enum=ConfigMap;Secret;RawObject
	Kind string `json:"kind"`

	// ServiceAccountName names the ServiceAccount in the CR's own namespace
	// whose identity the operator impersonates when applying and deleting the
	// RawObject output. Authorization is plain Kubernetes RBAC granted to that
	// ServiceAccount (get/create/patch/delete on the target kind).
	// Required for RawObject outputs; must not be set for ConfigMap/Secret.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Name is the name of the output resource.
	// Defaults to the JinjaTemplate CR name if omitted.
	// Must not be set for RawObject outputs (the name comes from the rendered
	// manifest's metadata.name).
	// +optional
	Name string `json:"name,omitempty"`

	// Key is the data key in the output ConfigMap or Secret where the rendered
	// template content is stored. Defaults to "content" if omitted.
	// Ignored when Keys is set.
	// +optional
	Key string `json:"key,omitempty"`

	// Keys defines a list of individual key-template pairs to write into the
	// output Secret or ConfigMap. When set, the top-level Template/TemplateFrom
	// fields and Output.Key are ignored; each entry is rendered independently
	// using the same sources context and the rendered value is trimmed of
	// leading/trailing whitespace before being written.
	// +optional
	Keys []OutputKey `json:"keys,omitempty"`
}

// OutputKey defines a single key/value pair in a multi-key output.
// Exactly one of Template or TemplateFrom must be provided.
type OutputKey struct {
	// Key is the data key in the output ConfigMap or Secret.
	Key string `json:"key"`

	// Template is an inline Jinja template string rendered as this key's value.
	// +optional
	Template string `json:"template,omitempty"`

	// TemplateFrom references an external template stored in a ConfigMap for
	// this key's value.
	// +optional
	TemplateFrom *TemplateFrom `json:"templateFrom,omitempty"`
}

// OutputRef stores a reference to a previously created output resource.
type OutputRef struct {
	// Kind is the kind of the output resource. For RawObject outputs this is
	// the actual kind of the rendered manifest (e.g. GlobalNetworkPolicy).
	Kind string `json:"kind"`

	// Name is the name of the output resource.
	Name string `json:"name"`

	// APIVersion is the apiVersion of the output resource. Empty for
	// ConfigMap/Secret outputs; set for RawObject outputs.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// Namespace is the namespace of the output resource. Set for namespaced
	// RawObject outputs; empty for cluster-scoped RawObject outputs and for
	// ConfigMap/Secret outputs (which always live in the CR's namespace).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ServiceAccountName is the ServiceAccount identity (in the CR's
	// namespace) that created this output. Cleanup after a target or
	// ServiceAccount change runs under this identity. Set for RawObject
	// outputs only.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// JinjaTemplateStatus defines the observed state of a JinjaTemplate.
type JinjaTemplateStatus struct {
	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastOutput records the most recently created output resource.
	// Used to detect output target changes and clean up the old resource.
	// +optional
	LastOutput *OutputRef `json:"lastOutput,omitempty"`

	// DNSSources tracks the resolved state of each DNS source, keyed by
	// source name. Persists last known records across lookups and operator
	// restarts; drives stale-on-error behavior and removal grace periods.
	// +optional
	DNSSources []DNSSourceStatus `json:"dnsSources,omitempty"`
}

// DNSSourceStatus records the resolved state of a single DNS source.
type DNSSourceStatus struct {
	// Name is the source name from spec.sources.
	Name string `json:"name"`

	// Records are the currently effective records, including those held
	// through a removal grace period.
	// +optional
	Records []DNSRecord `json:"records,omitempty"`

	// LastSuccessfulLookup is the time of the last successful DNS query.
	// +optional
	LastSuccessfulLookup *metav1.Time `json:"lastSuccessfulLookup,omitempty"`

	// LastError is the error of the most recent failed lookup, empty on success.
	// +optional
	LastError string `json:"lastError,omitempty"`
}

// DNSRecord is one resolved value with bookkeeping for grace-period removal.
type DNSRecord struct {
	// Value is the resolved IP address.
	Value string `json:"value"`

	// LastSeen is the last time this value appeared in a lookup response.
	LastSeen metav1.Time `json:"lastSeen"`
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
