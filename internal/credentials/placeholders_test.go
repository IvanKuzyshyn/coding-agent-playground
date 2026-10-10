package credentials

import (
	"encoding/json"
	"testing"
)

func TestSecretFromBearer(t *testing.T) {
	secret := NewSecret()
	cases := []struct {
		name   string
		header string
		ok     bool
	}{
		{"claude placeholder", "Bearer " + ClaudeToken(secret), true},
		{"codex placeholder", "Bearer " + CodexAccessToken(secret), true},
		{"no bearer prefix", ClaudeToken(secret), false},
		{"claude short secret", "Bearer " + claudePrefix + "abc", false},
		{"random jwt", "Bearer a.b.c", false},
		{"real-looking claude token", "Bearer sk-ant-oat01-abcdef", false},
	}
	for _, c := range cases {
		got, ok := SecretFromBearer(c.header)
		if ok != c.ok || (ok && got != secret) {
			t.Errorf("%s: got (%q, %v)", c.name, got, ok)
		}
	}
}

func TestCodexAuthJSONShape(t *testing.T) {
	var a struct {
		AuthMode    string `json:"auth_mode"`
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	secret := NewSecret()
	if err := json.Unmarshal(CodexAuthJSON(secret, "2026-10-10T00:00:00Z"), &a); err != nil {
		t.Fatal(err)
	}
	if a.AuthMode != "chatgpt" || a.LastRefresh == "" || a.Tokens.AccountID != CodexAccountID {
		t.Fatalf("unexpected auth.json: %+v", a)
	}
	if got, ok := SecretFromBearer("Bearer " + a.Tokens.AccessToken); !ok || got != secret {
		t.Fatalf("unexpected auth.json: %+v", a)
	}
}
