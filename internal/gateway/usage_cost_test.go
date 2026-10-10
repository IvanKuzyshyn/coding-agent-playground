package gateway

import (
	"context"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"terrarium/internal/pricing"
)

func TestExpectedCodexChatter(t *testing.T) {
	for path, want := range map[string]bool{
		"/chatgpt/ps/plugins/installed":         true,
		"/chatgpt/api/codex/accounts/check":     true,
		"/chatgpt/api/codex/settings/user":      true,
		"/chatgpt/backend-api/me":               false,
		"/chatgpt/api/codex/tasks":              false,
		"/anthropic/api/oauth/profile":          false,
		"/chatgpt/ps/pluginsx/../../wham/usage": false,
	} {
		if got := expectedCodexChatter(path); got != want {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	}
}

func TestUsageTeeSniffsSSEWithoutContentType(t *testing.T) {
	stream := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":60},\"output_tokens\":7}}}\n\n"
	var got int64
	rc := newUsageTee(io.NopCloser(strings.NewReader(stream)), usageCodex, "", func(u pricing.Usage) { got = u.Input + u.CacheWrite + u.Output })
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if got != 47 {
		t.Errorf("usage: got %d want 47", got)
	}
}

type usageRecorder struct {
	Store
	tokens int64
	cost   float64
	priced bool
}

func (u *usageRecorder) AddUsage(_ context.Context, _ uuid.UUID, tokens int64, cost float64, priced bool) (int64, error) {
	u.tokens, u.cost, u.priced = tokens, cost, priced
	return tokens, nil
}

func TestAddUsagePricesByRequestModel(t *testing.T) {
	prices, err := pricing.Load(filepath.Join("..", "..", "config", "pricing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &usageRecorder{}
	g := &Gateway{Store: rec, Pricing: prices}
	u := pricing.Usage{Input: 1000, CacheWrite: 2000, CacheRead: 1_000_000, Output: 500}

	g.addUsage(uuid.New(), requestModel([]byte(`{"model":"claude-sonnet-5-5","max_tokens":10}`)), u)
	want := (1000*2.0 + 2000*2.5 + 1_000_000*0.10 + 500*10.0) / 1e6
	// Cache reads are cheap and do not count against the token budget.
	if rec.tokens != 3500 || !rec.priced || math.Abs(rec.cost-want) > 1e-12 {
		t.Errorf("priced: tokens %d cost %v priced %v, want 3500 %v true", rec.tokens, rec.cost, rec.priced, want)
	}
	g.addUsage(uuid.New(), requestModel([]byte(`{"model":"mystery-1"}`)), u)
	if rec.priced || rec.cost != 0 || rec.tokens != 3500 {
		t.Errorf("unknown model: %+v", rec)
	}
}
