package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewOperatorConfig(t *testing.T) {
	cfg := NewOperatorConfig()
	assert.True(t, cfg.DefaultOwnerReference)
}

func TestShouldSetOwnerReference(t *testing.T) {
	cfg := &OperatorConfig{DefaultOwnerReference: true}

	// No override → use default (true)
	assert.True(t, cfg.ShouldSetOwnerReference(nil))

	// Override true
	boolTrue := true
	assert.True(t, cfg.ShouldSetOwnerReference(&boolTrue))

	// Override false
	boolFalse := false
	assert.False(t, cfg.ShouldSetOwnerReference(&boolFalse))

	// Default false, no override
	cfg.DefaultOwnerReference = false
	assert.False(t, cfg.ShouldSetOwnerReference(nil))

	// Default false, override true
	assert.True(t, cfg.ShouldSetOwnerReference(&boolTrue))
}
