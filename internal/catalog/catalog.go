// Package catalog loads presets and resolves them into a frozen session.Spec.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"terrarium/internal/session"
)

type presetFile struct {
	Defaults preset            `yaml:"defaults"`
	Presets  map[string]preset `yaml:"presets"`
}

type preset struct {
	Image     string `yaml:"image"`
	Resources struct {
		CPUs     float64 `yaml:"cpus"`
		MemoryMB int64   `yaml:"memory_mb"`
		PIDs     int64   `yaml:"pids"`
	} `yaml:"resources"`
	Network struct {
		Allow []string `yaml:"allow"`
	} `yaml:"network"`
	Limits struct {
		MaxAgentSteps      int   `yaml:"max_agent_steps"`
		TokenBudget        int64 `yaml:"token_budget"`
		IdleTimeoutSeconds int   `yaml:"idle_timeout_seconds"`
		TTLSeconds         int   `yaml:"ttl_seconds"`
	} `yaml:"limits"`
	Instructions string `yaml:"instructions"`
}

type Catalog struct {
	file   presetFile
	digest string
}

func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f presetFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if _, ok := f.Presets["default"]; !ok {
		return nil, fmt.Errorf("%s: preset %q is required", path, "default")
	}
	sum := sha256.Sum256(b)
	return &Catalog{file: f, digest: hex.EncodeToString(sum[:])}, nil
}

func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.file.Presets))
	for n := range c.file.Presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

type Request struct {
	Harness string
	Preset  string
	Repo    string
	Model   string
}

// Resolve merges defaults with the named preset and freezes the result. Preset values override
// defaults field by field; a preset's network.allow replaces the default list.
func (c *Catalog) Resolve(r Request) (session.Spec, error) {
	if r.Harness != session.HarnessClaude && r.Harness != session.HarnessCodex {
		return session.Spec{}, fmt.Errorf("unknown harness %q", r.Harness)
	}
	name := r.Preset
	if name == "" {
		name = "default"
	}
	p, ok := c.file.Presets[name]
	if !ok {
		return session.Spec{}, fmt.Errorf("unknown preset %q", name)
	}
	d := c.file.Defaults
	spec := session.Spec{
		Harness:      r.Harness,
		Preset:       name,
		Image:        pick(p.Image, d.Image),
		Repo:         r.Repo,
		Model:        r.Model,
		Instructions: pick(p.Instructions, d.Instructions),
		Resources: session.Resources{
			CPUs:     pick(p.Resources.CPUs, d.Resources.CPUs),
			MemoryMB: pick(p.Resources.MemoryMB, d.Resources.MemoryMB),
			PIDs:     pick(p.Resources.PIDs, d.Resources.PIDs),
		},
		Network: session.Network{Allow: d.Network.Allow},
		Limits: session.Limits{
			MaxAgentSteps:      pick(p.Limits.MaxAgentSteps, d.Limits.MaxAgentSteps),
			TokenBudget:        pick(p.Limits.TokenBudget, d.Limits.TokenBudget),
			IdleTimeoutSeconds: pick(p.Limits.IdleTimeoutSeconds, d.Limits.IdleTimeoutSeconds),
			TTLSeconds:         pick(p.Limits.TTLSeconds, d.Limits.TTLSeconds),
		},
		CatalogDigest: c.digest,
	}
	if p.Network.Allow != nil {
		spec.Network.Allow = p.Network.Allow
	}
	if spec.Image == "" || spec.Limits.TTLSeconds <= 0 || spec.Limits.TokenBudget <= 0 {
		return session.Spec{}, fmt.Errorf("preset %q: image, ttl_seconds and token_budget are required", name)
	}
	return spec, nil
}

func pick[T comparable](v, fallback T) T {
	var zero T
	if v != zero {
		return v
	}
	return fallback
}
