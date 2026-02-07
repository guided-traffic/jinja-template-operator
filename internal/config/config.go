// Package config provides operator-level configuration.
package config

// OperatorConfig holds global configuration for the operator.
type OperatorConfig struct {
	// DefaultOwnerReference is the global default for whether generated resources
	// should have an OwnerReference pointing to the JinjaTemplate CR.
	// Individual CRs can override this via spec.setOwnerReference.
	DefaultOwnerReference bool
}

// NewOperatorConfig creates an OperatorConfig with default values.
func NewOperatorConfig() *OperatorConfig {
	return &OperatorConfig{
		DefaultOwnerReference: true,
	}
}

// ShouldSetOwnerReference returns whether an OwnerReference should be set,
// considering the CR-level override and the global default.
func (c *OperatorConfig) ShouldSetOwnerReference(crOverride *bool) bool {
	if crOverride != nil {
		return *crOverride
	}
	return c.DefaultOwnerReference
}
