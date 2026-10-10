package catalog

import (
	"os"
	"path/filepath"
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
	if spec.Network.Allow == nil || len(spec.Network.Allow) != 0 {
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

func TestResolveLifecycle(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := c.Resolve(Request{Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Lifecycle.OnComplete != "terminate" || spec.Lifecycle.OnFailure != "terminate" || spec.Lifecycle.OnStop != "keep_idle" {
		t.Errorf("default lifecycle: %+v", spec.Lifecycle)
	}
	spec, _ = c.Resolve(Request{Harness: "codex", KeepIdle: true})
	if spec.Lifecycle.OnComplete != "keep_idle" || spec.Lifecycle.OnFailure != "keep_idle" || spec.Lifecycle.OnStop != "keep_idle" {
		t.Errorf("keep_idle lifecycle: %+v", spec.Lifecycle)
	}
}

func TestResolveRejectsUnknownLifecycleAction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "defaults: {image: x, resources: {cpus: 1, memory_mb: 1, pids: 1}, limits: {ttl_seconds: 1, token_budget: 1},\n" +
		"  lifecycle: {on_complete: explode, on_failure: terminate, on_stop: keep_idle}}\npresets:\n  default: {}\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(Request{Harness: "codex"}); err == nil {
		t.Error("unknown lifecycle action accepted")
	}
}
