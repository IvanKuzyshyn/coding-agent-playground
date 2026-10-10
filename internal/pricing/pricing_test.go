package pricing

import (
	"math"
	"path/filepath"
	"testing"
)

func TestCost(t *testing.T) {
	tbl, err := Load(filepath.Join("..", "..", "config", "pricing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		model  string
		u      Usage
		want   float64
		priced bool
	}{
		// 1M input at $2 + 1M output at $10
		{"claude-sonnet-5-5", Usage{Input: 1e6, Output: 1e6}, 12, true},
		// Claude Code alias suffix and a dated id resolve to the base entry
		{"claude-sonnet-5-5[1m]", Usage{CacheRead: 1e6}, 0.10, true},
		{"claude-haiku-4-5-20251001", Usage{Output: 1e6}, 5, true},
		// long-context tier applies to the whole request above the threshold
		{"gpt-6.1-sol", Usage{Input: 100000, CacheRead: 200000, Output: 1000}, (100000*4.0 + 200000*0.20 + 1000*15.0) / 1e6, true},
		{"gpt-6.1-sol", Usage{Input: 100000, Output: 1000}, (100000*2.0 + 1000*10.0) / 1e6, true},
		{"some-unknown-model", Usage{Input: 1}, 0, false},
		// a prefix must end at a dash: claude-opus-5 must not price claude-opus-55
		{"claude-opus-55", Usage{Input: 1}, 0, false},
	}
	for _, c := range cases {
		got, ok := tbl.Cost(c.model, c.u)
		if ok != c.priced || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s %+v: got (%v, %v) want (%v, %v)", c.model, c.u, got, ok, c.want, c.priced)
		}
	}
}
