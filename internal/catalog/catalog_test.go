package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolveMergesPresetOverDefaults(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := c.Resolve(Request{Harness: "codex", Preset: "explore", Repo: "octo/hello"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Limits.TokenBudget != 500000 || spec.Limits.MaxAgentSteps != 30 {
		t.Errorf("preset limits not applied: %+v", spec.Limits)
	}
	if spec.Limits.TTLSeconds != 14400 || spec.Image != "terrarium-agent:local" {
		t.Errorf("defaults not applied: %+v", spec)
	}
	if !slices.Equal(spec.Network.Allow, []string{"github.com", "codeload.github.com"}) {
		t.Errorf("preset allowlist should replace defaults: %v", spec.Network.Allow)
	}
	if spec.CatalogDigest == "" {
		t.Error("missing digest")
	}
}

func TestResolveRejectsUnknown(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(Request{Harness: "gpt-shell"}); err == nil {
		t.Error("unknown harness accepted")
	}
	if _, err := c.Resolve(Request{Harness: "codex", Preset: "nope"}); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestLoadRequiresDefaultPreset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(p, []byte("presets:\n  other: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("expected error without default preset")
	}
}
