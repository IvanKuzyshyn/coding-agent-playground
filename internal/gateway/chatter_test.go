package gateway

import (
	"io"
	"strings"
	"testing"
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
	rc := newUsageTee(io.NopCloser(strings.NewReader(stream)), usageCodex, "", func(n int64) { got = n })
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if got != 47 {
		t.Errorf("usage: got %d want 47", got)
	}
}
