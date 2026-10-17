package config

import (
	"path/filepath"
	"testing"
)

func TestLayering(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUCTOR_HOME", dir)
	if err := SetValue("models.standard.effort", "medium", false); err != nil {
		t.Fatal(err)
	}
	if err := SetValue("models.standard.effort", "low", true); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Models.Standard.Effort != "low" || c.Models.Standard.Model != "claude-sonnet-5-5" {
		t.Fatalf("local should win per key: %+v", c.Models.Standard)
	}
	if err := UnsetValue("models.standard.effort", true); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if c.Models.Standard.Effort != "medium" {
		t.Fatalf("synced value should apply after unset, got %q", c.Models.Standard.Effort)
	}
	if got := Target(true); got != filepath.Join(dir, "local.toml") {
		t.Fatalf("local target %s", got)
	}
}

func TestSetStringValidates(t *testing.T) {
	t.Setenv("CONDUCTOR_HOME", t.TempDir())
	for _, tc := range []struct{ key, val string }{
		{"git.push", "maybe"},
		{"parallel.max_workers", "0"},
		{"parallel.max_workers", "x"},
		{"sync.auto", "sometimes"},
		{"no.such.key", "1"},
		{"profile", "economy"},
	} {
		if err := SetString(tc.key, tc.val, false); err == nil {
			t.Errorf("%s=%s should be rejected", tc.key, tc.val)
		}
	}
	if err := SetString("providers.enabled", "codex, nvidia", false); err != nil {
		t.Fatal(err)
	}
	c, _ := Load()
	if len(c.Providers.Enabled) != 2 || c.Providers.Enabled[1] != "nvidia" {
		t.Fatalf("list not parsed: %v", c.Providers.Enabled)
	}
}

func TestDefaultsAreNeutral(t *testing.T) {
	t.Setenv("CONDUCTOR_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Comms.Language != "en" || c.Comms.Address != "" || c.Git.Commit != "ask" {
		t.Fatalf("unexpected defaults: %+v %+v", c.Comms, c.Git)
	}
}
