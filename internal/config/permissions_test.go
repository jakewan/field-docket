package config

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// TestConfigReadsThePermissionAllowlist covers the key that lets an operator
// keep working with a store whose files are reachable beyond their owner.
//
// It is a list of paths rather than a single switch on purpose: an exemption
// recorded once during an incident would otherwise stay in force for every
// store the binary ever opens, including one the config is pointed at later.
func TestConfigReadsThePermissionAllowlist(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "absent key exempts nothing",
			body: "store: /somewhere/field-docket.db\n",
			want: nil,
		},
		{
			name: "absent file exempts nothing",
			body: "",
			want: nil,
		},
		{
			name: "one path",
			body: "allow_unsafe_permissions:\n  - /srv/shared/field-docket.db\n",
			want: []string{"/srv/shared/field-docket.db"},
		},
		{
			name: "several paths",
			body: "allow_unsafe_permissions:\n  - /a/one.db\n  - /b/two.db\n",
			want: []string{"/a/one.db", "/b/two.db"},
		},
		{
			name: "inline sequence",
			body: "allow_unsafe_permissions: [/a/one.db, /b/two.db]\n",
			want: []string{"/a/one.db", "/b/two.db"},
		},
		{
			name: "blank entries carry no intent and are dropped",
			body: "allow_unsafe_permissions:\n  - /a/one.db\n  - \"   \"\n  - \"\"\n",
			want: []string{"/a/one.db"},
		},
		{
			name: "surrounding whitespace does not leak into the path",
			body: "allow_unsafe_permissions:\n  - \"  /a/one.db  \"\n",
			want: []string{"/a/one.db"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			t.Setenv("FIELD_DOCKET_CONFIG", "")
			if tt.body != "" {
				writeConfig(t, filepath.Join(dir, "field-docket"), tt.body)
			}

			cfg, err := Load(context.Background(), "")
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if !slices.Equal(cfg.AllowUnsafePermissions, tt.want) {
				t.Errorf("allow_unsafe_permissions = %v, want %v", cfg.AllowUnsafePermissions, tt.want)
			}
		})
	}
}

// TestConfigExpandsTildeInTheAllowlist keeps the allowlist usable with the same
// path spelling the store key accepts. An entry that failed to expand would not
// error — it would silently match nothing, leaving the operator with a store
// that keeps refusing and a config that looks correct.
func TestConfigExpandsTildeInTheAllowlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("FIELD_DOCKET_CONFIG", "")
	writeConfig(t, filepath.Join(dir, "field-docket"), "allow_unsafe_permissions:\n  - ~/field-docket.db\n")

	cfg, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := []string{filepath.Join(home, "field-docket.db")}
	if !slices.Equal(cfg.AllowUnsafePermissions, want) {
		t.Errorf("allow_unsafe_permissions = %v, want %v", cfg.AllowUnsafePermissions, want)
	}
}
