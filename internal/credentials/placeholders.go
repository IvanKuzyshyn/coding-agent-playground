// Package credentials derives the per-session placeholders a sandbox holds and lets the gateway
// map them back to a session. A session has one random secret; the sandbox only ever sees forms
// derived from it, and the database stores only its sha256.
package credentials

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const (
	claudePrefix       = "sk-ant-oat01-terrarium-"
	codexRefreshPrefix = "terrarium-refresh-"
)

// CodexAccountID is the fake account id placed in the sandbox; the gateway replaces it.
const CodexAccountID = "acct-terrarium-placeholder"

// ProxyUser is the Basic-auth user name for the CONNECT proxy; the password is the secret.
const ProxyUser = "terrarium"

// NewSecret returns a random 256-bit secret, hex encoded.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// ClaudeToken is the value of CLAUDE_CODE_OAUTH_TOKEN inside the sandbox.
func ClaudeToken(secret string) string { return claudePrefix + secret }

// CodexAuthJSON is the sandbox's ~/.codex/auth.json: unsigned JWTs with only the claims Codex
// 0.162.x parses (see spikes/s2-codex-swap). Codex never verifies the signature.
func CodexAuthJSON(secret, lastRefreshRFC3339 string) []byte {
	id := jwt(map[string]any{
		"email": "agent@terrarium.local",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": CodexAccountID,
		},
	})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   lastRefreshRFC3339,
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  CodexAccessToken(secret),
			"refresh_token": codexRefreshPrefix + secret,
			"account_id":    CodexAccountID,
		},
	})
	return b
}

// CodexAccessToken has a far-future exp so Codex never refreshes on its own.
func CodexAccessToken(secret string) string {
	return jwt(map[string]any{"exp": 4102444800, "terrarium_secret": secret})
}

// SecretFromBearer extracts the session secret from either placeholder form.
func SecretFromBearer(authorization string) (string, bool) {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok {
		return "", false
	}
	if s, ok := strings.CutPrefix(token, claudePrefix); ok {
		return s, validSecret(s)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		Secret string `json:"terrarium_secret"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", false
	}
	return claims.Secret, validSecret(claims.Secret)
}

// SecretFromCodexRefreshToken maps the placeholder refresh token Codex sends to the gateway's
// /oauth/token endpoint back to the session secret.
func SecretFromCodexRefreshToken(token string) (string, bool) {
	s, ok := strings.CutPrefix(token, codexRefreshPrefix)
	return s, ok && validSecret(s)
}

// ProxyURL is the HTTPS_PROXY value for the sandbox.
func ProxyURL(proxyHostPort, secret string) string {
	return "http://" + ProxyUser + ":" + secret + "@" + proxyHostPort
}

func validSecret(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func jwt(claims map[string]any) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	return header + "." + enc.EncodeToString(payload) + ".terrarium"
}

// DeriveSecret computes a session's gateway secret from the server key, so the supervisor can
// rebuild placeholders after a restart while the database stores only the hash.
func DeriveSecret(key []byte, sessionID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("gateway-secret:" + sessionID))
	return hex.EncodeToString(m.Sum(nil))
}
