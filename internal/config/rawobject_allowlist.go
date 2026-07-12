package config

import (
	"fmt"
	"os"
	"slices"

	"sigs.k8s.io/yaml"
)

// RawObjectAllowlistEntry grants a set of namespaces permission to render
// RawObject outputs of the listed kinds. Namespace names and kinds are matched
// exactly (no wildcards).
type RawObjectAllowlistEntry struct {
	// Namespaces are the namespaces of JinjaTemplate CRs this entry applies to.
	Namespaces []string `json:"namespaces"`

	// Kinds are the object kinds CRs in these namespaces may render.
	Kinds []RawObjectKind `json:"kinds"`
}

// RawObjectKind identifies an allowed output object type.
type RawObjectKind struct {
	// APIVersion of the allowed kind (e.g. "crd.projectcalico.org/v1").
	APIVersion string `json:"apiVersion"`

	// Kind of the allowed object (e.g. "GlobalNetworkPolicy").
	Kind string `json:"kind"`
}

// LoadRawObjectAllowlist reads and parses an allowlist YAML file. The file
// contains a list of RawObjectAllowlistEntry items. An empty path returns an
// empty (deny-all) allowlist.
func LoadRawObjectAllowlist(path string) ([]RawObjectAllowlistEntry, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator CLI flag, not user input
	if err != nil {
		return nil, fmt.Errorf("failed to read raw object allowlist file %q: %w", path, err)
	}

	var entries []RawObjectAllowlistEntry
	if err := yaml.UnmarshalStrict(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse raw object allowlist file %q: %w", path, err)
	}

	if err := validateRawObjectAllowlist(entries); err != nil {
		return nil, fmt.Errorf("invalid raw object allowlist file %q: %w", path, err)
	}

	return entries, nil
}

// validateRawObjectAllowlist rejects entries with empty fields, which would
// otherwise silently never match (or, worse, be mistaken for wildcards).
func validateRawObjectAllowlist(entries []RawObjectAllowlistEntry) error {
	for i, entry := range entries {
		if len(entry.Namespaces) == 0 {
			return fmt.Errorf("entry %d: namespaces must not be empty", i)
		}
		if len(entry.Kinds) == 0 {
			return fmt.Errorf("entry %d: kinds must not be empty", i)
		}
		for j, ns := range entry.Namespaces {
			if ns == "" {
				return fmt.Errorf("entry %d: namespaces[%d] must not be empty", i, j)
			}
		}
		for j, kind := range entry.Kinds {
			if kind.APIVersion == "" || kind.Kind == "" {
				return fmt.Errorf("entry %d: kinds[%d] must set both apiVersion and kind", i, j)
			}
		}
	}
	return nil
}

// IsRawObjectAllowed reports whether a JinjaTemplate in the given namespace
// may render a RawObject of the given apiVersion/kind. Default deny: without
// a matching allowlist entry the answer is false.
func (c *OperatorConfig) IsRawObjectAllowed(namespace, apiVersion, kind string) bool {
	for _, entry := range c.RawObjectAllowlist {
		if !slices.Contains(entry.Namespaces, namespace) {
			continue
		}
		for _, k := range entry.Kinds {
			if k.APIVersion == apiVersion && k.Kind == kind {
				return true
			}
		}
	}
	return false
}
