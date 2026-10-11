// Package gateway holds the real harness and GitHub credentials. It swaps per-session
// placeholders for them on an exact allowlist of inference routes and on git/PR routes for the
// session's repository, enforces the session token budget, and runs the CONNECT egress proxy.
// See docs/contracts/gateway.md.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/credentials"
	"terrarium/internal/pricing"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// Production upstreams.
const (
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultCodexBaseURL     = "https://chatgpt.com/backend-api/codex"
	DefaultCodexRefreshURL  = "https://auth.openai.com/oauth/token"
)

// Codex request bodies are buffered so they can be replayed after a refresh on upstream 401.
const maxReplayBody = 10 << 20

// Store is the subset of *store.Store the gateway uses.
type Store interface {
	GatewaySessionBySecretHash(ctx context.Context, hash []byte) (store.GatewaySession, error)
	AddUsage(ctx context.Context, id uuid.UUID, tokens int64, costUSD float64, priced bool) (int64, error)
	AppendEvent(ctx context.Context, sessionID uuid.UUID, kind string, turnSeq *int, payload any) error
}

// Gateway serves the inference listener (Handler) and the CONNECT proxy (ProxyHandler).
// Set fields before the first call to either; zero values get production defaults.
type Gateway struct {
	Store Store
	// Pricing estimates API-equivalent cost per response; nil leaves costs unpriced.
	Pricing         *pricing.Table
	ClaudeTokenFile string
	CodexAuthFile   string
	GitHubTokenFile string

	AnthropicBaseURL string
	CodexBaseURL     string
	CodexRefreshURL  string
	GitHubBaseURL    string
	GitHubAPIBaseURL string
	// Transport carries upstream inference, GitHub and Codex refresh requests.
	Transport http.RoundTripper
	// Dial opens CONNECT tunnels.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Now  func() time.Time

	initOnce      sync.Once
	claude        *fileToken
	codex         *codexCreds
	github        *fileToken
	anthropic     *url.URL
	codexBase     *url.URL
	githubBase    *url.URL
	githubAPIBase *url.URL
	proxy         *httputil.ReverseProxy
	githubProxy   *httputil.ReverseProxy
}

type route struct {
	harness string
	// upstreamPath is appended to the harness base URL. Empty for routes answered locally.
	upstreamPath string
	usage        usageKind
}

// Exact method+path allowlist. The real tokens may carry more than inference scope, so no other
// upstream path ever gets them.
var routes = map[string]route{
	"POST /anthropic/v1/messages":              {harness: session.HarnessClaude, upstreamPath: "/v1/messages", usage: usageClaude},
	"POST /anthropic/v1/messages/count_tokens": {harness: session.HarnessClaude, upstreamPath: "/v1/messages/count_tokens"},
	"POST /openai/responses":                   {harness: session.HarnessCodex, upstreamPath: "/responses", usage: usageCodex},
	"GET /openai/models":                       {harness: session.HarnessCodex, upstreamPath: "/models"},
	"POST /oauth/token":                        {harness: session.HarnessCodex},
}

type ctxKey struct{}

// reqInfo travels with a forwarded request from the handler to the transport and ModifyResponse.
type reqInfo struct {
	route     route
	sessionID uuid.UUID
	body      []byte // Codex only, for replay
	model     string // from the request body, for pricing
}

var errNoCredential = errors.New("real credential unavailable")

func (g *Gateway) init() {
	g.initOnce.Do(func() {
		if g.AnthropicBaseURL == "" {
			g.AnthropicBaseURL = DefaultAnthropicBaseURL
		}
		if g.CodexBaseURL == "" {
			g.CodexBaseURL = DefaultCodexBaseURL
		}
		if g.CodexRefreshURL == "" {
			g.CodexRefreshURL = DefaultCodexRefreshURL
		}
		if g.GitHubBaseURL == "" {
			g.GitHubBaseURL = DefaultGitHubBaseURL
		}
		if g.GitHubAPIBaseURL == "" {
			g.GitHubAPIBaseURL = DefaultGitHubAPIBaseURL
		}
		if g.Transport == nil {
			g.Transport = http.DefaultTransport.(*http.Transport).Clone()
		}
		if g.Dial == nil {
			g.Dial = (&net.Dialer{}).DialContext
		}
		if g.Now == nil {
			g.Now = time.Now
		}
		g.anthropic = mustParseURL(g.AnthropicBaseURL)
		g.codexBase = mustParseURL(g.CodexBaseURL)
		g.githubBase = mustParseURL(g.GitHubBaseURL)
		g.githubAPIBase = mustParseURL(g.GitHubAPIBaseURL)
		g.claude = &fileToken{path: g.ClaudeTokenFile}
		g.github = &fileToken{path: g.GitHubTokenFile}
		g.codex = &codexCreds{
			path:       g.CodexAuthFile,
			refreshURL: g.CodexRefreshURL,
			client:     &http.Client{Transport: g.Transport, Timeout: 30 * time.Second},
			now:        g.Now,
		}
		g.proxy = &httputil.ReverseProxy{
			Rewrite:        g.rewrite,
			Transport:      credentialTransport{g},
			FlushInterval:  -1,
			ModifyResponse: g.modifyResponse,
			ErrorHandler:   g.proxyError,
		}
		g.githubProxy = g.newGitHubProxy()
		if _, ok := g.claude.get(); !ok {
			slog.Warn("claude credential not loaded; claude-code routes return 503 until it is", "file", g.ClaudeTokenFile)
		}
	})
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// Handler serves the inference listener.
func (g *Gateway) Handler() http.Handler {
	g.init()
	return http.HandlerFunc(g.serveInference)
}

func (g *Gateway) serveInference(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" && r.URL.RawPath == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/github/") {
		g.serveGitHub(w, r)
		return
	}
	ctx := r.Context()
	rt, ok := routes[r.Method+" "+r.URL.Path]
	// RawPath is set only for encoded paths; rejecting it means no encoded or dot-segment variant
	// can ever match.
	if !ok || r.URL.RawPath != "" {
		slog.Info("inference route denied", "method", r.Method, "path", truncate(r.URL.Path))
		if secret, ok := credentials.SecretFromBearer(r.Header.Get("Authorization")); ok && !expectedCodexChatter(r.URL.Path) {
			if s, err := g.Store.GatewaySessionBySecretHash(ctx, credentials.Hash(secret)); err == nil {
				g.denyInference(ctx, s.ID, r, "route")
			}
		}
		writeError(w, http.StatusForbidden, "route not allowed")
		return
	}

	var secret string
	if rt.upstreamPath == "" {
		secret, ok = refreshTokenSecret(r)
	} else {
		secret, ok = credentials.SecretFromBearer(r.Header.Get("Authorization"))
	}
	if !ok {
		writeError(w, http.StatusForbidden, "missing or unknown credential")
		return
	}
	s, err := g.Store.GatewaySessionBySecretHash(ctx, credentials.Hash(secret))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "missing or unknown credential")
		return
	}
	if err != nil {
		slog.Error("session lookup", "err", err)
		writeError(w, http.StatusServiceUnavailable, "session lookup failed")
		return
	}
	if s.Harness != rt.harness {
		g.denyInference(ctx, s.ID, r, "harness")
		writeError(w, http.StatusForbidden, "route not allowed for this session")
		return
	}
	if !session.Live(s.Status) {
		g.denyInference(ctx, s.ID, r, "status")
		writeError(w, http.StatusForbidden, "session not live")
		return
	}
	if rt.upstreamPath == "" {
		g.servePlaceholderRefresh(w, secret)
		return
	}
	if s.TokensUsed >= s.TokenBudget {
		g.denyInference(ctx, s.ID, r, "budget")
		writeError(w, http.StatusTooManyRequests, "session token budget exhausted")
		return
	}
	if !g.hasCredential(rt.harness) {
		writeError(w, http.StatusServiceUnavailable, "credential unavailable")
		return
	}

	info := &reqInfo{route: rt, sessionID: s.ID}
	// Codex bodies are kept for replay after a token refresh; priced routes are read for the model.
	if rt.harness == session.HarnessCodex || rt.usage != usageNone {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReplayBody+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "read request body")
			return
		}
		if len(body) > maxReplayBody {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if rt.harness == session.HarnessCodex {
			info.body = body
		}
		info.model = requestModel(body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
	}
	slog.Info("inference forward", "session", s.ID, "method", r.Method, "path", r.URL.Path)
	g.proxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKey{}, info)))
}

func (g *Gateway) hasCredential(harness string) bool {
	if harness == session.HarnessClaude {
		_, ok := g.claude.get()
		return ok
	}
	return g.codex.available()
}

// Codex calls these chatgpt_base_url endpoints on every run (plugins, settings, account check;
// spikes/s2-codex-swap) and works without them. They are denied but not recorded as egress
// events, so the engineer's egress view shows only unexpected attempts.
var codexChatterPrefixes = []string{
	"/chatgpt/ps/plugins/",
	"/chatgpt/plugins/",
	"/chatgpt/api/codex/accounts/check",
	"/chatgpt/api/codex/settings/user",
	"/chatgpt/api/codex/ps/mcp",
}

func expectedCodexChatter(path string) bool {
	for _, p := range codexChatterPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (g *Gateway) denyInference(ctx context.Context, id uuid.UUID, r *http.Request, reason string) {
	g.event(ctx, id, map[string]any{
		"decision": "denied",
		"via":      "inference",
		"method":   r.Method,
		"path":     truncate(r.URL.Path),
		"reason":   reason,
	})
}

// event records an egress decision. A failed write is logged and never fails the request.
func (g *Gateway) event(ctx context.Context, id uuid.UUID, payload map[string]any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := g.Store.AppendEvent(ctx, id, store.EventEgress, nil, payload); err != nil {
		slog.Error("append egress event", "session", id, "err", err)
	}
}

// refreshTokenSecret reads Codex's refresh request. Codex sends JSON; form is accepted too.
func refreshTokenSecret(r *http.Request) (string, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return "", false
	}
	var token string
	var j struct {
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(body, &j) == nil {
		token = j.RefreshToken
	} else if q, err := url.ParseQuery(string(body)); err == nil {
		token = q.Get("refresh_token")
	}
	return credentials.SecretFromCodexRefreshToken(token)
}

func (g *Gateway) servePlaceholderRefresh(w http.ResponseWriter, secret string) {
	var a struct {
		Tokens struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(credentials.CodexAuthJSON(secret, g.Now().UTC().Format(time.RFC3339)), &a); err != nil {
		writeError(w, http.StatusInternalServerError, "placeholder unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id_token":      a.Tokens.IDToken,
		"access_token":  a.Tokens.AccessToken,
		"refresh_token": a.Tokens.RefreshToken,
	})
}

func (g *Gateway) rewrite(pr *httputil.ProxyRequest) {
	info := pr.In.Context().Value(ctxKey{}).(*reqInfo)
	base := g.anthropic
	if info.route.harness == session.HarnessCodex {
		base = g.codexBase
	}
	pr.Out.URL.Scheme = base.Scheme
	pr.Out.URL.Host = base.Host
	pr.Out.URL.Path = strings.TrimSuffix(base.Path, "/") + info.route.upstreamPath
	pr.Out.URL.RawPath = ""
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.Out.Host = ""
	pr.Out.Header.Del("Authorization")
	pr.Out.Header.Del("X-Api-Key")
	pr.Out.Header.Del("ChatGPT-Account-ID")
	// Without a client Accept-Encoding the transport negotiates gzip itself and decompresses, so
	// usage parsing always sees plain text (clients may ask for br or zstd).
	pr.Out.Header.Del("Accept-Encoding")
}

func (g *Gateway) modifyResponse(resp *http.Response) error {
	info := resp.Request.Context().Value(ctxKey{}).(*reqInfo)
	slog.Info("upstream response", "session", info.sessionID, "path", resp.Request.URL.Path, "status", resp.StatusCode,
		"content_type", resp.Header.Get("Content-Type"), "content_encoding", resp.Header.Get("Content-Encoding"))
	if info.route.usage != usageNone {
		resp.Body = newUsageTee(resp.Body, info.route.usage, resp.Header.Get("Content-Type"), func(u pricing.Usage) {
			g.addUsage(info.sessionID, info.model, u)
		})
	}
	return nil
}

// addUsage charges the budget with uncached input + cache writes + output (cache reads are cheap
// and excluded) and adds the response's API-equivalent cost.
func (g *Gateway) addUsage(id uuid.UUID, model string, u pricing.Usage) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens := u.Input + u.CacheWrite + u.Output
	cost, priced := g.Pricing.Cost(model, u)
	total, err := g.Store.AddUsage(ctx, id, tokens, cost, priced)
	if err != nil {
		slog.Error("add usage", "session", id, "tokens", tokens, "err", err)
		return
	}
	slog.Info("tokens used", "session", id, "model", model, "tokens", tokens, "cost_usd", cost, "priced", priced, "total", total)
}

// requestModel reads the "model" field of an inference request body; empty if absent.
func requestModel(body []byte) string {
	var b struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Model
}

func (g *Gateway) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNoCredential) {
		writeError(w, http.StatusServiceUnavailable, "credential unavailable")
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Error("upstream request", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "upstream request failed")
}

// credentialTransport attaches the real credential at the last moment, so it never passes
// through Rewrite, and implements Codex's refresh-and-retry on 401.
type credentialTransport struct{ g *Gateway }

func (t credentialTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g := t.g
	info := req.Context().Value(ctxKey{}).(*reqInfo)
	if info.route.harness == session.HarnessClaude {
		token, ok := g.claude.get()
		if !ok {
			return nil, errNoCredential
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return g.Transport.RoundTrip(req)
	}

	access, account, err := g.codex.current(req.Context())
	if err != nil {
		slog.Error("codex credential", "err", err)
		return nil, errNoCredential
	}
	setCodexAuth(req, access, account)
	resp, err := g.Transport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	// Keep the 401 so it can be returned unchanged if the refresh fails.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Del("Content-Length")

	access, account, err = g.codex.refreshAfter401(req.Context(), access)
	if err != nil {
		slog.Error("codex refresh after 401", "err", err)
		return resp, nil
	}
	retry := req.Clone(req.Context())
	retry.Body = http.NoBody
	if len(info.body) > 0 {
		retry.Body = io.NopCloser(bytes.NewReader(info.body))
	}
	retry.ContentLength = int64(len(info.body))
	retry.GetBody = nil
	setCodexAuth(retry, access, account)
	slog.Info("codex retry after refresh", "session", info.sessionID)
	return g.Transport.RoundTrip(retry)
}

func setCodexAuth(req *http.Request, access, account string) {
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("ChatGPT-Account-ID", account)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// truncate bounds attacker-controlled strings before they reach logs and events.
func truncate(s string) string {
	if len(s) > 256 {
		return s[:256]
	}
	return s
}
