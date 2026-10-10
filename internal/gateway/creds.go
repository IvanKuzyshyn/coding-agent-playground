package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// codexClientID is Codex's OAuth client id (codex-rs/login/src/auth/manager.rs, rust-v0.162.1).
const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// refreshBefore is how close to expiry the Codex access token is refreshed proactively.
const refreshBefore = time.Hour

// claudeCreds holds the Max token. The file is read on first use and re-tried while missing, so
// the token can be provisioned without restarting the gateway.
type claudeCreds struct {
	path  string
	mu    sync.Mutex
	token string
}

func (c *claudeCreds) get() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" && c.path != "" {
		if b, err := os.ReadFile(c.path); err == nil {
			c.token = strings.TrimSpace(string(b))
		}
	}
	return c.token, c.token != ""
}

// codexCreds holds the gateway's own Codex login. The gateway is its only refresher. One mutex
// serialises load and refresh, which makes refresh single-flight: callers waiting on it see the
// rotated token and do not refresh again.
type codexCreds struct {
	path       string
	refreshURL string
	client     *http.Client
	now        func() time.Time

	mu        sync.Mutex
	loaded    bool
	access    string
	refresh   string
	accountID string
	exp       time.Time // zero if the access token has no readable exp
}

func (c *codexCreds) available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked() == nil
}

// current returns the access token and account id, refreshing first if the token expires within
// refreshBefore. A failed proactive refresh falls back to the old token while it is still valid.
func (c *codexCreds) current(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return "", "", err
	}
	if !c.exp.IsZero() && c.exp.Sub(c.now()) < refreshBefore {
		if err := c.refreshLocked(ctx); err != nil {
			if !c.now().Before(c.exp) {
				return "", "", err
			}
			slog.Warn("codex proactive refresh failed; using current token", "err", err)
		}
	}
	return c.access, c.accountID, nil
}

// refreshAfter401 refreshes unless another caller already replaced the token that got the 401.
func (c *codexCreds) refreshAfter401(ctx context.Context, rejected string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return "", "", err
	}
	if c.access == rejected {
		if err := c.refreshLocked(ctx); err != nil {
			return "", "", err
		}
	}
	return c.access, c.accountID, nil
}

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

func (c *codexCreds) loadLocked() error {
	if c.loaded {
		return nil
	}
	if c.path == "" {
		return errNoCredential
	}
	b, err := os.ReadFile(c.path)
	if err != nil {
		return fmt.Errorf("read codex auth file: %w", err)
	}
	var a struct {
		Tokens codexTokens `json:"tokens"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return errors.New("codex auth file is not valid JSON")
	}
	if a.Tokens.AccessToken == "" || a.Tokens.RefreshToken == "" || a.Tokens.AccountID == "" {
		return errors.New("codex auth file lacks tokens.access_token, refresh_token or account_id")
	}
	c.access = a.Tokens.AccessToken
	c.refresh = a.Tokens.RefreshToken
	c.accountID = a.Tokens.AccountID
	c.exp = jwtExp(c.access)
	c.loaded = true
	return nil
}

// refreshLocked performs the same request as Codex's request_chatgpt_token_refresh: a JSON POST
// of client_id, grant_type=refresh_token and refresh_token. Response fields are all optional.
func (c *codexCreds) refreshLocked(ctx context.Context) error {
	// Detached from the caller: aborting after the server rotated the refresh token but before
	// write-back would lose the login.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": c.refresh,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.refreshURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("codex refresh request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// The body may echo token details; only the status is logged.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("codex refresh: status %d", resp.StatusCode)
	}
	var r struct {
		IDToken      *string `json:"id_token"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return errors.New("codex refresh: invalid response")
	}
	if r.AccessToken == nil || *r.AccessToken == "" {
		return errors.New("codex refresh: response has no access_token")
	}
	c.access = *r.AccessToken
	c.exp = jwtExp(c.access)
	if r.RefreshToken != nil && *r.RefreshToken != "" {
		c.refresh = *r.RefreshToken
	}
	slog.Info("codex credential refreshed", "expires", c.exp)
	// The new tokens are already in memory; a failed write only loses them across a restart.
	if err := c.writeBack(r.IDToken, r.AccessToken, r.RefreshToken); err != nil {
		slog.Error("codex auth write-back", "err", err)
	}
	return nil
}

// writeBack updates the token fields and last_refresh in auth.json, leaving every other field as
// it was, via temp file + rename so a crash never leaves a partial file.
func (c *codexCreds) writeBack(idToken, accessToken, refreshToken *string) error {
	b, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return errors.New("codex auth file is not a JSON object")
	}
	var tokens map[string]json.RawMessage
	if raw, ok := doc["tokens"]; ok {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return errors.New("codex auth file tokens is not a JSON object")
		}
	}
	if tokens == nil {
		tokens = map[string]json.RawMessage{}
	}
	for k, v := range map[string]*string{"id_token": idToken, "access_token": accessToken, "refresh_token": refreshToken} {
		if v != nil && *v != "" {
			tokens[k], _ = json.Marshal(*v)
		}
	}
	doc["tokens"], _ = json.Marshal(tokens)
	doc["last_refresh"], _ = json.Marshal(c.now().UTC().Format(time.RFC3339Nano))
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".auth.json.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// jwtExp reads the exp claim without verifying the signature; the token is our own.
func jwtExp(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(claims.Exp), 0)
}
