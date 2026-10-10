package gateway

import "testing"

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
