package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAllowlistFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allowlist.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadRawObjectAllowlistEmptyPath(t *testing.T) {
	entries, err := LoadRawObjectAllowlist("")
	require.NoError(t, err)
	assert.Nil(t, entries)
}

func TestLoadRawObjectAllowlistMissingFile(t *testing.T) {
	_, err := LoadRawObjectAllowlist(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read")
}

func TestLoadRawObjectAllowlistValid(t *testing.T) {
	path := writeAllowlistFile(t, `
- namespaces:
    - infra
    - platform
  kinds:
    - apiVersion: crd.projectcalico.org/v1
      kind: GlobalNetworkPolicy
- namespaces:
    - team-a
  kinds:
    - apiVersion: v1
      kind: ConfigMap
    - apiVersion: rbac.authorization.k8s.io/v1
      kind: Role
`)

	entries, err := LoadRawObjectAllowlist(path)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, []string{"infra", "platform"}, entries[0].Namespaces)
	assert.Equal(t, "GlobalNetworkPolicy", entries[0].Kinds[0].Kind)
	assert.Len(t, entries[1].Kinds, 2)
}

func TestLoadRawObjectAllowlistInvalidYAML(t *testing.T) {
	path := writeAllowlistFile(t, "not: [valid")
	_, err := LoadRawObjectAllowlist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestLoadRawObjectAllowlistUnknownField(t *testing.T) {
	path := writeAllowlistFile(t, `
- namespaces: ["infra"]
  kinds:
    - apiVersion: v1
      kind: ConfigMap
  wildcard: true
`)
	_, err := LoadRawObjectAllowlist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestLoadRawObjectAllowlistValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "no namespaces",
			content: "- kinds:\n    - apiVersion: v1\n      kind: ConfigMap\n",
			wantErr: "namespaces must not be empty",
		},
		{
			name:    "no kinds",
			content: "- namespaces: [\"infra\"]\n",
			wantErr: "kinds must not be empty",
		},
		{
			name:    "empty namespace",
			content: "- namespaces: [\"\"]\n  kinds:\n    - apiVersion: v1\n      kind: ConfigMap\n",
			wantErr: "namespaces[0] must not be empty",
		},
		{
			name:    "missing apiVersion",
			content: "- namespaces: [\"infra\"]\n  kinds:\n    - kind: ConfigMap\n",
			wantErr: "must set both apiVersion and kind",
		},
		{
			name:    "missing kind",
			content: "- namespaces: [\"infra\"]\n  kinds:\n    - apiVersion: v1\n",
			wantErr: "must set both apiVersion and kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeAllowlistFile(t, tt.content)
			_, err := LoadRawObjectAllowlist(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestIsRawObjectAllowed(t *testing.T) {
	cfg := &OperatorConfig{
		RawObjectAllowlist: []RawObjectAllowlistEntry{
			{
				Namespaces: []string{"infra", "platform"},
				Kinds: []RawObjectKind{
					{APIVersion: "crd.projectcalico.org/v1", Kind: "GlobalNetworkPolicy"},
				},
			},
			{
				Namespaces: []string{"team-a"},
				Kinds: []RawObjectKind{
					{APIVersion: "v1", Kind: "ConfigMap"},
				},
			},
		},
	}

	assert.True(t, cfg.IsRawObjectAllowed("infra", "crd.projectcalico.org/v1", "GlobalNetworkPolicy"))
	assert.True(t, cfg.IsRawObjectAllowed("platform", "crd.projectcalico.org/v1", "GlobalNetworkPolicy"))
	assert.True(t, cfg.IsRawObjectAllowed("team-a", "v1", "ConfigMap"))

	// Kind not granted to this namespace (bound to another entry)
	assert.False(t, cfg.IsRawObjectAllowed("team-a", "crd.projectcalico.org/v1", "GlobalNetworkPolicy"))
	assert.False(t, cfg.IsRawObjectAllowed("infra", "v1", "ConfigMap"))

	// Unlisted namespace, wrong apiVersion, wrong kind
	assert.False(t, cfg.IsRawObjectAllowed("other", "crd.projectcalico.org/v1", "GlobalNetworkPolicy"))
	assert.False(t, cfg.IsRawObjectAllowed("infra", "crd.projectcalico.org/v2", "GlobalNetworkPolicy"))
	assert.False(t, cfg.IsRawObjectAllowed("infra", "crd.projectcalico.org/v1", "NetworkPolicy"))
}

func TestIsRawObjectAllowedEmptyAllowlist(t *testing.T) {
	cfg := NewOperatorConfig()
	assert.False(t, cfg.IsRawObjectAllowed("default", "v1", "ConfigMap"))
}
