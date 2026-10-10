// Package pricing estimates the API-equivalent cost of one model response from list prices
// (config/pricing.yaml). Subscription runs are not billed per token; the estimate is for comparing
// subscription and API economics.
package pricing

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rates are USD per 1M tokens.
type Rates struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheWrite float64 `yaml:"cache_write"`
	CacheRead  float64 `yaml:"cache_read"`
}

type modelPrice struct {
	Rates `yaml:",inline"`
	Long  *struct {
		Above int64 `yaml:"above"`
		Rates `yaml:",inline"`
	} `yaml:"long"`
}

type Table struct {
	models map[string]modelPrice
}

func Load(path string) (*Table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Models map[string]modelPrice `yaml:"models"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &Table{models: f.Models}, nil
}

// Usage is one response's token counts, each category counted once (input excludes cached and
// cache-written tokens).
type Usage struct {
	Input, CacheWrite, CacheRead, Output int64
}

// Cost returns the USD estimate and whether the model was priced. Lookup strips Claude Code's
// "[1m]" suffix and falls back to the longest table key that prefixes the model id, which covers
// dated ids such as claude-haiku-4-5-20251001.
func (t *Table) Cost(model string, u Usage) (float64, bool) {
	if t == nil {
		return 0, false
	}
	p, ok := t.lookup(model)
	if !ok {
		return 0, false
	}
	r := p.Rates
	if p.Long != nil && u.Input+u.CacheRead+u.CacheWrite > p.Long.Above {
		r = p.Long.Rates
	}
	return (float64(u.Input)*r.Input + float64(u.CacheWrite)*r.CacheWrite +
		float64(u.CacheRead)*r.CacheRead + float64(u.Output)*r.Output) / 1e6, true
}

func (t *Table) lookup(model string) (modelPrice, bool) {
	model = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "[1m]")
	if p, ok := t.models[model]; ok {
		return p, true
	}
	best := ""
	for k := range t.models {
		if strings.HasPrefix(model, k+"-") && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return modelPrice{}, false
	}
	return t.models[best], true
}
