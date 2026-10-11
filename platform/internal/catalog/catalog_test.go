package catalog

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

func TestResolveMergesPresetOverDefaults(t *testing.T) {
	g := NewWithT(t)
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	spec, err := c.Resolve(Request{Harness: "codex", Preset: "explore", Repo: "octo/hello"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(spec.Limits.TokenBudget).To(Equal(int64(500000)), "preset limits not applied")
	g.Expect(spec.Limits.MaxAgentSteps).To(Equal(30), "preset limits not applied")
	g.Expect(spec.Limits.TTLSeconds).To(Equal(14400), "defaults not applied")
	g.Expect(spec.Image).To(Equal("terrarium-agent:local"), "defaults not applied")
	g.Expect(spec.Network.Allow).NotTo(BeNil(), "preset allowlist should replace defaults")
	g.Expect(spec.Network.Allow).To(BeEmpty(), "preset allowlist should replace defaults")
	g.Expect(spec.CatalogDigest).NotTo(BeEmpty(), "missing digest")
}

func TestResolveRejectsUnknown(t *testing.T) {
	g := NewWithT(t)
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	_, err = c.Resolve(Request{Harness: "gpt-shell"})
	g.Expect(err).To(HaveOccurred(), "unknown harness accepted")
	_, err = c.Resolve(Request{Harness: "codex", Preset: "nope"})
	g.Expect(err).To(HaveOccurred(), "unknown preset accepted")
}

func TestLoadRequiresDefaultPreset(t *testing.T) {
	g := NewWithT(t)
	p := filepath.Join(t.TempDir(), "p.yaml")
	g.Expect(os.WriteFile(p, []byte("presets:\n  other: {}\n"), 0o600)).To(Succeed())
	_, err := Load(p)
	g.Expect(err).To(HaveOccurred(), "expected error without default preset")
}

func TestResolveLifecycle(t *testing.T) {
	g := NewWithT(t)
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	spec, err := c.Resolve(Request{Harness: "codex"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(spec.Lifecycle.OnComplete).To(Equal("terminate"), "default lifecycle")
	g.Expect(spec.Lifecycle.OnFailure).To(Equal("terminate"), "default lifecycle")
	g.Expect(spec.Lifecycle.OnStop).To(Equal("keep_idle"), "default lifecycle")
	spec, _ = c.Resolve(Request{Harness: "codex", KeepIdle: true})
	g.Expect(spec.Lifecycle.OnComplete).To(Equal("keep_idle"), "keep_idle lifecycle")
	g.Expect(spec.Lifecycle.OnFailure).To(Equal("keep_idle"), "keep_idle lifecycle")
	g.Expect(spec.Lifecycle.OnStop).To(Equal("keep_idle"), "keep_idle lifecycle")
}

func TestResolveRejectsUnknownLifecycleAction(t *testing.T) {
	g := NewWithT(t)
	p := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "defaults: {image: x, resources: {cpus: 1, memory_mb: 1, pids: 1}, limits: {ttl_seconds: 1, token_budget: 1},\n" +
		"  lifecycle: {on_complete: explode, on_failure: terminate, on_stop: keep_idle}}\npresets:\n  default: {}\n"
	g.Expect(os.WriteFile(p, []byte(yaml), 0o600)).To(Succeed())
	c, err := Load(p)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = c.Resolve(Request{Harness: "codex"})
	g.Expect(err).To(HaveOccurred(), "unknown lifecycle action accepted")
}

func TestResolveGuard(t *testing.T) {
	g := NewWithT(t)
	c, err := Load(filepath.Join("..", "..", "config", "presets.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	spec, err := c.Resolve(Request{Harness: "codex"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(spec.Guard).To(Equal("monitor"), "default guard")

	p := filepath.Join(t.TempDir(), "p.yaml")
	g.Expect(os.WriteFile(p, []byte("defaults:\n  image: i\n  resources: { cpus: 1, memory_mb: 1, pids: 1 }\n  limits: { token_budget: 1, ttl_seconds: 1 }\n  lifecycle: { on_complete: terminate, on_failure: terminate, on_stop: terminate }\n  guard: monitor\npresets:\n  default: {}\n  strict: { guard: block }\n  bad: { guard: loud }\n"), 0o600)).To(Succeed())
	c, err = Load(p)
	g.Expect(err).NotTo(HaveOccurred())
	spec, err = c.Resolve(Request{Harness: "codex", Preset: "strict"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(spec.Guard).To(Equal("block"), "preset guard overrides default")
	_, err = c.Resolve(Request{Harness: "codex", Preset: "bad"})
	g.Expect(err).To(MatchError(ContainSubstring("guard")), "unknown guard mode accepted")
}
