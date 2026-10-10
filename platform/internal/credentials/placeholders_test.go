package credentials

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"
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
		t.Run(c.name, func(t *testing.T) {
			g := NewWithT(t)
			got, ok := SecretFromBearer(c.header)
			g.Expect(ok).To(Equal(c.ok))
			if ok {
				g.Expect(got).To(Equal(secret))
			}
		})
	}
}

func TestCodexAuthJSONShape(t *testing.T) {
	g := NewWithT(t)
	var a struct {
		AuthMode    string `json:"auth_mode"`
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	secret := NewSecret()
	g.Expect(json.Unmarshal(CodexAuthJSON(secret, "2026-10-10T00:00:00Z"), &a)).To(Succeed())
	g.Expect(a.AuthMode).To(Equal("chatgpt"))
	g.Expect(a.LastRefresh).NotTo(BeEmpty())
	g.Expect(a.Tokens.AccountID).To(Equal(CodexAccountID))
	got, ok := SecretFromBearer("Bearer " + a.Tokens.AccessToken)
	g.Expect(ok).To(BeTrue(), "access token is not a placeholder: %+v", a)
	g.Expect(got).To(Equal(secret))
}
