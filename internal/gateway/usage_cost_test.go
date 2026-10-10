package gateway

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"

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
		t.Run(path, func(t *testing.T) {
			NewWithT(t).Expect(expectedCodexChatter(path)).To(Equal(want))
		})
	}
}

func TestUsageTeeSniffsSSEWithoutContentType(t *testing.T) {
	g := NewWithT(t)
	stream := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":60},\"output_tokens\":7}}}\n\n"
	var got int64
	rc := newUsageTee(io.NopCloser(strings.NewReader(stream)), usageCodex, "", func(u pricing.Usage) { got = u.Input + u.CacheWrite + u.Output })
	_, err := io.Copy(io.Discard, rc)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(got).To(Equal(int64(47)), "usage")
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
	g := NewWithT(t)
	prices, err := pricing.Load(filepath.Join("..", "..", "config", "pricing.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rec := &usageRecorder{}
	gw := &Gateway{Store: rec, Pricing: prices}
	u := pricing.Usage{Input: 1000, CacheWrite: 2000, CacheRead: 1_000_000, Output: 500}

	gw.addUsage(uuid.New(), requestModel([]byte(`{"model":"claude-sonnet-5-5","max_tokens":10}`)), u)
	want := (1000*2.0 + 2000*2.5 + 1_000_000*0.10 + 500*10.0) / 1e6
	// Cache reads are cheap and do not count against the token budget.
	g.Expect(rec.tokens).To(Equal(int64(3500)), "priced tokens")
	g.Expect(rec.priced).To(BeTrue(), "priced")
	g.Expect(rec.cost).To(BeNumerically("~", want, 1e-12), "priced cost")
	gw.addUsage(uuid.New(), requestModel([]byte(`{"model":"mystery-1"}`)), u)
	g.Expect(rec.priced).To(BeFalse(), "unknown model priced")
	g.Expect(rec.cost).To(BeZero(), "unknown model cost")
	g.Expect(rec.tokens).To(Equal(int64(3500)), "unknown model tokens")
}
