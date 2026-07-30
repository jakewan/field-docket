package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes body to a config file under dir and returns its path.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestConfigDefaultsWhenAbsent is the behaviour that makes the server usable out
// of the box. No field is required, so an absent file is not an error — it means
// "use the defaults", and the composition root resolves them.
func TestConfigDefaultsWhenAbsent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FIELD_DOCKET_CONFIG", "")

	cfg, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("a missing config must not be fatal: %v", err)
	}
	if cfg.Store != "" {
		t.Errorf("store = %q, want empty so the caller applies its default", cfg.Store)
	}
}

func TestConfigResolvesStorePathPrecedence(t *testing.T) {
	xdg := t.TempDir()
	envDir := t.TempDir()
	flagDir := t.TempDir()

	writeConfig(t, filepath.Join(xdg, "field-docket"), "store: /from/xdg.db\n")
	envPath := writeConfig(t, envDir, "store: /from/env.db\n")
	flagPath := writeConfig(t, flagDir, "store: /from/flag.db\n")

	tests := []struct {
		name     string
		override string
		env      string
		want     string
	}{
		{"xdg when nothing else is set", "", "", "/from/xdg.db"},
		{"env beats xdg", "", envPath, "/from/env.db"},
		{"flag beats env", flagPath, envPath, "/from/flag.db"},
		{"whitespace override is ignored", "   ", envPath, "/from/env.db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			t.Setenv("FIELD_DOCKET_CONFIG", tt.env)

			cfg, err := Load(context.Background(), tt.override)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.Store != tt.want {
				t.Errorf("store = %q, want %q", cfg.Store, tt.want)
			}
		})
	}
}

// TestConfigRejectsAnUnreadableNamedConfig draws the line the absent-file case
// does not: a config the operator explicitly named and that exists but does not
// parse is an error, because silently ignoring a typo in it is worse than
// refusing to start.
func TestConfigRejectsAnUnreadableNamedConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "store: [this is not a string\n")
	t.Setenv("FIELD_DOCKET_CONFIG", path)

	if _, err := Load(context.Background(), ""); err == nil {
		t.Fatal("a malformed config was accepted")
	}
}

func TestConfigTreatsABlankStoreAsAbsent(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "store: \"   \"\n")
	t.Setenv("FIELD_DOCKET_CONFIG", path)

	cfg, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Store != "" {
		t.Errorf("store = %q, want empty: a blank value carries no intent", cfg.Store)
	}
}

func TestConfigExpandsALeadingTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	path := writeConfig(t, dir, "store: ~/docket/field-docket.db\n")
	t.Setenv("FIELD_DOCKET_CONFIG", path)

	cfg, err := Load(context.Background(), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join(home, "docket", "field-docket.db")
	if cfg.Store != want {
		t.Errorf("store = %q, want %q", cfg.Store, want)
	}
}
