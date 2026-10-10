package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/credentials"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

const (
	realClaudeToken  = "real-claude-token-SECRET"
	realCodexRefresh = "real-codex-refresh-SECRET"
	realAccountID    = "acct-real-SECRET"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.DiscardHandler))
	}
	os.Exit(m.Run())
}

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu       sync.Mutex
	sessions map[string]*store.GatewaySession
	events   []fakeEvent
	failApp  bool
}

type fakeEvent struct {
	session uuid.UUID
	kind    string
	payload map[string]any
}

func newFakeStore() *fakeStore { return &fakeStore{sessions: map[string]*store.GatewaySession{}} }

func (f *fakeStore) GatewaySessionBySecretHash(_ context.Context, hash []byte) (store.GatewaySession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[string(hash)]
	if !ok {
		return store.GatewaySession{}, store.ErrNotFound
	}
	return *s, nil
}

func (f *fakeStore) AddTokensUsed(_ context.Context, id uuid.UUID, n int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id {
			s.TokensUsed += n
			return s.TokensUsed, nil
		}
	}
	return 0, store.ErrNotFound
}

func (f *fakeStore) AppendEvent(_ context.Context, id uuid.UUID, kind string, _ *int, payload any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failApp {
		return fmt.Errorf("db down")
	}
	f.events = append(f.events, fakeEvent{id, kind, payload.(map[string]any)})
	return nil
}

func (f *fakeStore) add(harness, status string, budget, used int64, allow ...string) (string, uuid.UUID) {
	secret := credentials.NewSecret()
	id := uuid.New()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[string(credentials.Hash(secret))] = &store.GatewaySession{
		ID: id, Status: status, Harness: harness, Allow: allow, TokenBudget: budget, TokensUsed: used,
	}
	return secret, id
}

func (f *fakeStore) used(id uuid.UUID) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == id {
			return s.TokensUsed
		}
	}
	return -1
}

func (f *fakeStore) eventsFor(id uuid.UUID) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, e := range f.events {
		if e.session == id && e.kind == store.EventEgress {
			out = append(out, e.payload)
		}
	}
	return out
}

// env wires a gateway to fake upstreams. Swap the handler fields per test.
type env struct {
	t         *testing.T
	st        *fakeStore
	gw        *Gateway
	inference *httptest.Server
	proxy     *httptest.Server
	authFile  string

	anthropic atomic.Pointer[http.HandlerFunc]
	codex     atomic.Pointer[http.HandlerFunc]
	refresh   atomic.Pointer[http.HandlerFunc]
	upstreamN atomic.Int64
	refreshN  atomic.Int64
}

func delegate(p *atomic.Pointer[http.HandlerFunc], n *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h := p.Load()
		if h == nil {
			http.Error(w, "no handler", http.StatusTeapot)
			return
		}
		(*h)(w, r)
	})
}

func (e *env) setAnthropic(h http.HandlerFunc) { e.anthropic.Store(&h) }
func (e *env) setCodex(h http.HandlerFunc)     { e.codex.Store(&h) }
func (e *env) setRefresh(h http.HandlerFunc)   { e.refresh.Store(&h) }

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func realAccess(name string, exp time.Time) string {
	return fakeJWT(map[string]any{"exp": exp.Unix(), "n": name})
}

func writeAuthFile(t *testing.T, path, access string) {
	t.Helper()
	doc := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "2026-01-01T00:00:00Z",
		"unknown_top":    map[string]any{"keep": true},
		"tokens": map[string]any{
			"id_token":      "old-id-token",
			"access_token":  access,
			"refresh_token": realCodexRefresh,
			"account_id":    realAccountID,
			"unknown_tok":   "keep-me",
		},
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, st Store, codexAccess string) *env {
	t.Helper()
	e := &env{t: t}
	if fs, ok := st.(*fakeStore); ok {
		e.st = fs
	}
	anthropic := httptest.NewServer(delegate(&e.anthropic, &e.upstreamN))
	codex := httptest.NewServer(delegate(&e.codex, &e.upstreamN))
	refresh := httptest.NewServer(delegate(&e.refresh, &e.refreshN))
	t.Cleanup(anthropic.Close)
	t.Cleanup(codex.Close)
	t.Cleanup(refresh.Close)

	dir := t.TempDir()
	claudeFile := filepath.Join(dir, "claude_token")
	if err := os.WriteFile(claudeFile, []byte("  "+realClaudeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.authFile = filepath.Join(dir, "auth.json")
	if codexAccess != "" {
		writeAuthFile(t, e.authFile, codexAccess)
	}

	echo := echoServer(t)
	e.gw = &Gateway{
		Store:            st,
		ClaudeTokenFile:  claudeFile,
		CodexAuthFile:    e.authFile,
		AnthropicBaseURL: anthropic.URL,
		CodexBaseURL:     codex.URL + "/backend-api/codex",
		CodexRefreshURL:  refresh.URL + "/oauth/token",
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "example.test:443" {
				addr = echo
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	e.inference = httptest.NewServer(e.gw.Handler())
	e.proxy = httptest.NewServer(e.gw.ProxyHandler())
	t.Cleanup(e.inference.Close)
	t.Cleanup(e.proxy.Close)
	return e
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (e *env) do(method, path, auth, body string, hdr ...string) resp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.inference.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

// assertNoSecrets checks nothing real reached the sandbox.
func assertNoSecrets(t *testing.T, r resp, extra ...string) {
	t.Helper()
	all := r.body
	for k, v := range r.header {
		all += k + strings.Join(v, ",")
	}
	for _, s := range append([]string{realClaudeToken, realCodexRefresh, realAccountID}, extra...) {
		if strings.Contains(all, s) {
			t.Fatalf("response leaks a real credential")
		}
	}
}

func claudeBearer(secret string) string { return "Bearer " + credentials.ClaudeToken(secret) }
func codexBearer(secret string) string  { return "Bearer " + credentials.CodexAccessToken(secret) }

// waitFor polls because usage is reported when the proxy closes the upstream body, which can
// happen just after the client has read the last byte.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, newFakeStore(), "")
	r := e.do("GET", "/healthz", "", "")
	if r.status != 200 || strings.TrimSpace(r.body) != `{"ok":true}` {
		t.Fatalf("got %d %q", r.status, r.body)
	}
}

func TestClaudeSwapAndJSONUsage(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, "")
	secret, id := st.add(session.HarnessClaude, session.StatusRunning, 1000, 0)
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.URL.RawQuery != "beta=true" {
			t.Errorf("upstream got %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+realClaudeToken {
			t.Error("upstream did not get the real token")
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("X-Api-Key forwarded")
		}
		if r.Header.Get("anthropic-beta") != "oauth-2025-04-20,x" {
			t.Errorf("anthropic-beta = %q", r.Header.Get("anthropic-beta"))
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"model":"m"}` {
			t.Errorf("body = %q", b)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","usage":{"input_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":100,"output_tokens":5}}`)
	})
	r := e.do("POST", "/anthropic/v1/messages?beta=true", claudeBearer(secret), `{"model":"m"}`,
		"anthropic-beta", "oauth-2025-04-20,x", "X-Api-Key", "sk-ant-bogus")
	if r.status != 200 {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	assertNoSecrets(t, r)
	waitFor(t, func() bool { return st.used(id) == 15 })

	// count_tokens forwards but does not count usage.
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"input_tokens":42}`)
	})
	if r := e.do("POST", "/anthropic/v1/messages/count_tokens", claudeBearer(secret), `{}`); r.status != 200 {
		t.Fatalf("count_tokens status %d", r.status)
	}
	time.Sleep(50 * time.Millisecond)
	if got := st.used(id); got != 15 {
		t.Fatalf("count_tokens changed usage to %d", got)
	}
}

func TestClaudeSSEUsage(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, "")
	secret, id := st.add(session.HarnessClaude, session.StatusIdle, 1000, 0)
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":999,"output_tokens":1}}}` + "\r\n\r\n" +
		"data: {not json\n\n" +
		"data: " + strings.Repeat("x", maxSSELine+10) + "\n\n" +
		`data: {"type":"content_block_delta","delta":{"text":"hi"}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":12}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":20}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Odd-sized writes split lines across reads.
		for i := 0; i < len(stream); i += 7 {
			io.WriteString(w, stream[i:min(i+7, len(stream))])
		}
	})
	r := e.do("POST", "/anthropic/v1/messages", claudeBearer(secret), `{}`)
	if r.status != 200 || r.body != stream {
		t.Fatalf("stream altered: status %d, len %d want %d", r.status, len(r.body), len(stream))
	}
	waitFor(t, func() bool { return st.used(id) == 35 })
}

func TestSSEIsNotBuffered(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, "")
	secret, _ := st.add(session.HarnessClaude, session.StatusRunning, 1000, 0)
	release := make(chan struct{})
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	})
	req, _ := http.NewRequest("POST", e.inference.URL+"/anthropic/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", claudeBearer(secret))
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if !strings.Contains(line, "ping") {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first chunk not streamed before upstream finished")
	}
	close(release)
}

func TestCodexSwapAndSSEUsage(t *testing.T) {
	st := newFakeStore()
	access := realAccess("a1", time.Now().Add(10*24*time.Hour))
	e := newEnv(t, st, access)
	secret, id := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	e.setCodex(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+access || r.Header.Get("ChatGPT-Account-ID") != realAccountID {
			t.Error("upstream did not get the real codex credentials")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /backend-api/codex/models":
			if r.URL.RawQuery != "client_version=0.162.1" {
				t.Errorf("query %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"models":[]}`)
		case "POST /backend-api/codex/responses":
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"input":"hi"}` {
				t.Errorf("body %q", b)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11250,\"input_tokens_details\":{\"cached_tokens\":9600},\"output_tokens\":5}}}\n\n")
		default:
			t.Errorf("unexpected upstream %s %s", r.Method, r.URL.Path)
		}
	})
	r := e.do("GET", "/openai/models?client_version=0.162.1", codexBearer(secret), "", "ChatGPT-Account-ID", credentials.CodexAccountID)
	if r.status != 200 {
		t.Fatalf("models %d %s", r.status, r.body)
	}
	assertNoSecrets(t, r, access)
	r = e.do("POST", "/openai/responses", codexBearer(secret), `{"input":"hi"}`, "ChatGPT-Account-ID", credentials.CodexAccountID)
	if r.status != 200 {
		t.Fatalf("responses %d %s", r.status, r.body)
	}
	assertNoSecrets(t, r, access)
	waitFor(t, func() bool { return st.used(id) == 1655 })
}

func TestDenials(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, realAccess("a1", time.Now().Add(240*time.Hour)))
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) { t.Errorf("upstream reached: %s", r.URL.Path) })
	e.setCodex(func(w http.ResponseWriter, r *http.Request) { t.Errorf("upstream reached: %s", r.URL.Path) })
	claude, claudeID := st.add(session.HarnessClaude, session.StatusRunning, 1000, 0)
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	stopped, stoppedID := st.add(session.HarnessClaude, session.StatusStopped, 1000, 0)
	prov, _ := st.add(session.HarnessClaude, session.StatusProvisioning, 1000, 0)

	cases := []struct {
		name, method, path, auth string
	}{
		{"unknown secret", "POST", "/anthropic/v1/messages", claudeBearer(credentials.NewSecret())},
		{"foreign bearer", "POST", "/anthropic/v1/messages", "Bearer sk-ant-oat01-somethingelse"},
		{"no auth", "POST", "/anthropic/v1/messages", ""},
		{"non-live stopped", "POST", "/anthropic/v1/messages", claudeBearer(stopped)},
		{"non-live provisioning", "POST", "/anthropic/v1/messages", claudeBearer(prov)},
		{"claude session on codex route", "POST", "/openai/responses", claudeBearer(claude)},
		{"codex session on claude route", "POST", "/anthropic/v1/messages", codexBearer(codex)},
		{"unknown route", "GET", "/anthropic/api/oauth/profile", claudeBearer(claude)},
		{"create api key", "POST", "/anthropic/api/oauth/claude_cli/create_api_key", claudeBearer(claude)},
		{"wrong method", "GET", "/anthropic/v1/messages", claudeBearer(claude)},
		{"wrong method codex", "POST", "/openai/models", codexBearer(codex)},
		{"encoded slash", "POST", "/anthropic/v1/messages%2F..%2F..%2Fapi%2Foauth%2Fprofile", claudeBearer(claude)},
		{"encoded char", "POST", "/anthropic/v1/m%65ssages", claudeBearer(claude)},
		{"dot segments", "POST", "/anthropic/v1/messages/../../api/oauth/profile", claudeBearer(claude)},
		{"dot segment to allowed", "POST", "/anthropic/v1/x/../messages", claudeBearer(claude)},
		{"trailing slash", "POST", "/anthropic/v1/messages/", claudeBearer(claude)},
		{"chatgpt route", "GET", "/chatgpt/api/codex/accounts/check", codexBearer(codex)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r := e.do(c.method, c.path, c.auth, "{}"); r.status != http.StatusForbidden {
				t.Fatalf("status %d %s", r.status, r.body)
			}
		})
	}
	if n := e.upstreamN.Load(); n != 0 {
		t.Fatalf("upstream called %d times", n)
	}
	reasons := func(id uuid.UUID) map[string]bool {
		m := map[string]bool{}
		for _, ev := range st.eventsFor(id) {
			if ev["decision"] != "denied" || ev["via"] != "inference" {
				t.Errorf("event %v", ev)
			}
			m[ev["reason"].(string)] = true
		}
		return m
	}
	if r := reasons(claudeID); !r["route"] || !r["harness"] {
		t.Fatalf("claude session events: %v", r)
	}
	if r := reasons(stoppedID); !r["status"] {
		t.Fatalf("stopped session events: %v", r)
	}
}

func TestBudgetExhausted(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, realAccess("a1", time.Now().Add(240*time.Hour)))
	claude, claudeID := st.add(session.HarnessClaude, session.StatusRunning, 100, 100)
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 100, 150)
	for _, c := range []struct{ path, auth string }{
		{"/anthropic/v1/messages", claudeBearer(claude)},
		{"/openai/responses", codexBearer(codex)},
	} {
		r := e.do("POST", c.path, c.auth, "{}")
		if r.status != http.StatusTooManyRequests || strings.TrimSpace(r.body) != `{"error":"session token budget exhausted"}` {
			t.Fatalf("%s: %d %s", c.path, r.status, r.body)
		}
	}
	if e.upstreamN.Load() != 0 {
		t.Fatal("upstream reached")
	}
	ev := st.eventsFor(claudeID)
	if len(ev) != 1 || ev[0]["reason"] != "budget" || ev[0]["decision"] != "denied" {
		t.Fatalf("events %v", ev)
	}
}

func TestEventWriteFailureDoesNotFailRequest(t *testing.T) {
	st := newFakeStore()
	st.failApp = true
	e := newEnv(t, st, "")
	claude, _ := st.add(session.HarnessClaude, session.StatusRunning, 100, 100)
	if r := e.do("POST", "/anthropic/v1/messages", claudeBearer(claude), "{}"); r.status != http.StatusTooManyRequests {
		t.Fatalf("status %d", r.status)
	}
}

func TestMissingCredentialIsPerHarness(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, "") // no codex auth.json
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	claude, _ := st.add(session.HarnessClaude, session.StatusRunning, 1000, 0)
	if r := e.do("POST", "/openai/responses", codexBearer(codex), "{}"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("codex status %d", r.status)
	}
	e.setAnthropic(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if r := e.do("POST", "/anthropic/v1/messages", claudeBearer(claude), "{}"); r.status != 200 {
		t.Fatalf("claude status %d", r.status)
	}

	// Credential provisioned later is picked up without restart.
	writeAuthFile(t, e.authFile, realAccess("a1", time.Now().Add(240*time.Hour)))
	e.setCodex(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if r := e.do("POST", "/openai/responses", codexBearer(codex), "{}"); r.status != 200 {
		t.Fatalf("codex after provisioning: %d", r.status)
	}

	// Missing Claude token: 503 for Claude only.
	e2 := newEnv(t, st, "")
	os.Remove(e2.gw.ClaudeTokenFile)
	e2.gw.claude.token = "" // drop the copy loaded at startup
	if r := e2.do("POST", "/anthropic/v1/messages", claudeBearer(claude), "{}"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("claude without token: %d", r.status)
	}
}

func TestOAuthTokenPlaceholder(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, realAccess("a1", time.Now().Add(240*time.Hour)))
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	claude, _ := st.add(session.HarnessClaude, session.StatusRunning, 1000, 0)
	for _, body := range []string{
		`{"client_id":"app_x","grant_type":"refresh_token","refresh_token":"terrarium-refresh-` + codex + `"}`,
		"grant_type=refresh_token&refresh_token=terrarium-refresh-" + codex,
	} {
		r := e.do("POST", "/oauth/token", "", body)
		if r.status != 200 {
			t.Fatalf("status %d %s", r.status, r.body)
		}
		assertNoSecrets(t, r)
		var got map[string]string
		if err := json.Unmarshal([]byte(r.body), &got); err != nil {
			t.Fatal(err)
		}
		if got["refresh_token"] != "terrarium-refresh-"+codex || got["access_token"] != credentials.CodexAccessToken(codex) || got["id_token"] == "" {
			t.Fatalf("unexpected tokens %v", got)
		}
	}
	if e.refreshN.Load() != 0 {
		t.Fatal("real refresh called")
	}
	if r := e.do("POST", "/oauth/token", "", `{"refresh_token":"terrarium-refresh-`+claude+`"}`); r.status != 403 {
		t.Fatalf("claude session refresh: %d", r.status)
	}
	if r := e.do("POST", "/oauth/token", "", `{"refresh_token":"`+realCodexRefresh+`"}`); r.status != 403 {
		t.Fatalf("foreign refresh: %d", r.status)
	}
}

// refreshHandler issues access tokens named r1, r2, ... and checks the request matches Codex's.
func refreshHandler(t *testing.T, e *env, delay time.Duration) http.HandlerFunc {
	var n atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/oauth/token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("refresh request %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		want := map[string]string{"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": realCodexRefresh}
		if fmt.Sprint(body) != fmt.Sprint(want) && body["refresh_token"] != "rotated-refresh-1" {
			t.Errorf("refresh body %v", body)
		}
		time.Sleep(delay)
		i := n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"id_token":      fmt.Sprintf("new-id-%d", i),
			"access_token":  realAccess(fmt.Sprintf("r%d", i), time.Now().Add(240*time.Hour)),
			"refresh_token": fmt.Sprintf("rotated-refresh-%d", i),
		})
	}
}

func jwtName(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		N string `json:"n"`
	}
	_ = json.Unmarshal(b, &c)
	return c.N
}

// checkWriteBack asserts auth.json holds the rotated tokens (access token named wantAccess) and
// every other field unchanged.
func checkWriteBack(t *testing.T, path, wantAccess string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var doc struct {
		AuthMode string          `json:"auth_mode"`
		APIKey   json.RawMessage `json:"OPENAI_API_KEY"`
		Unknown  map[string]any  `json:"unknown_top"`
		LastRefr string          `json:"last_refresh"`
		Tokens   map[string]any  `json:"tokens"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.AuthMode != "chatgpt" || string(doc.APIKey) != "null" || doc.Unknown["keep"] != true {
		t.Fatalf("top-level fields not preserved: %s", b)
	}
	if doc.Tokens["unknown_tok"] != "keep-me" || doc.Tokens["account_id"] != realAccountID {
		t.Fatalf("token fields not preserved: %s", b)
	}
	if access, _ := doc.Tokens["access_token"].(string); jwtName(access) != wantAccess || doc.Tokens["refresh_token"] != "rotated-refresh-1" || doc.Tokens["id_token"] != "new-id-1" {
		t.Fatalf("tokens not rotated: %s", b)
	}
	if doc.LastRefr == "2026-01-01T00:00:00Z" {
		t.Fatal("last_refresh not updated")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".auth.json.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestCodexProactiveRefresh(t *testing.T) {
	st := newFakeStore()
	old := realAccess("old", time.Now().Add(30*time.Minute))
	e := newEnv(t, st, old)
	e.setRefresh(refreshHandler(t, e, 0))
	var seen atomic.Value
	e.setCodex(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(jwtName(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		w.WriteHeader(200)
	})
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	if r := e.do("POST", "/openai/responses", codexBearer(codex), "{}"); r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	if e.refreshN.Load() != 1 || seen.Load() != "r1" {
		t.Fatalf("refreshes %d, upstream saw token %v", e.refreshN.Load(), seen.Load())
	}
	checkWriteBack(t, e.authFile, "r1")
	// Fresh token: no further refresh.
	e.do("POST", "/openai/responses", codexBearer(codex), "{}")
	if e.refreshN.Load() != 1 {
		t.Fatalf("refreshes %d", e.refreshN.Load())
	}
}

func TestCodexRefreshOn401Retries(t *testing.T) {
	st := newFakeStore()
	old := realAccess("old", time.Now().Add(240*time.Hour))
	e := newEnv(t, st, old)
	e.setRefresh(refreshHandler(t, e, 0))
	var calls atomic.Int64
	e.setCodex(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"input":"replay me"}` {
			t.Errorf("body %q", b)
		}
		if r.Header.Get("Authorization") == "Bearer "+old {
			http.Error(w, `{"error":"token expired"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	r := e.do("POST", "/openai/responses", codexBearer(codex), `{"input":"replay me"}`)
	if r.status != 200 || calls.Load() != 2 || e.refreshN.Load() != 1 {
		t.Fatalf("status %d, upstream calls %d, refreshes %d", r.status, calls.Load(), e.refreshN.Load())
	}
	checkWriteBack(t, e.authFile, "r1")

	// Refresh failure returns the original 401 and leaks nothing.
	e.setRefresh(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant","refresh_token":"`+realCodexRefresh+`"}`, 400)
	})
	e.setCodex(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	})
	r = e.do("POST", "/openai/responses", codexBearer(codex), `{"input":"replay me"}`)
	if r.status != 401 {
		t.Fatalf("status %d", r.status)
	}
	assertNoSecrets(t, r, "invalid_grant", "rotated-refresh")
}

func TestCodexRefreshSingleFlight(t *testing.T) {
	st := newFakeStore()
	old := realAccess("old", time.Now().Add(240*time.Hour))
	e := newEnv(t, st, old)
	e.setRefresh(refreshHandler(t, e, 50*time.Millisecond))
	e.setCodex(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+old {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(200)
	})
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	const n = 10
	var wg sync.WaitGroup
	statuses := make(chan int, n)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest("POST", e.inference.URL+"/openai/responses", strings.NewReader("{}"))
			req.Header.Set("Authorization", codexBearer(codex))
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body.Close()
			statuses <- r.StatusCode
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 200 {
			t.Fatalf("status %d", s)
		}
	}
	if got := e.refreshN.Load(); got != 1 {
		t.Fatalf("refreshes %d, want 1", got)
	}
}

func TestCodexProactiveRefreshSingleFlight(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, realAccess("old", time.Now().Add(10*time.Minute)))
	e.setRefresh(refreshHandler(t, e, 50*time.Millisecond))
	e.setCodex(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	codex, _ := st.add(session.HarnessCodex, session.StatusRunning, 1000, 0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", e.inference.URL+"/openai/models", nil)
			req.Header.Set("Authorization", codexBearer(codex))
			if r, err := http.DefaultClient.Do(req); err == nil {
				r.Body.Close()
			}
		}()
	}
	wg.Wait()
	if got := e.refreshN.Load(); got != 1 {
		t.Fatalf("refreshes %d, want 1", got)
	}
}

// connect sends a raw CONNECT with early bytes after the headers.
func connect(t *testing.T, proxyURL, method, target, auth, early string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Host
	}
	msg := method + " " + target + " HTTP/1.1\r\nHost: " + host + "\r\n"
	if auth != "" {
		msg += "Proxy-Authorization: " + auth + "\r\n"
	}
	msg += "\r\n" + early
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	return resp, c, br
}

func basic(secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials.ProxyUser+":"+secret))
}

func TestConnectProxy(t *testing.T) {
	st := newFakeStore()
	e := newEnv(t, st, "")
	secret, id := st.add(session.HarnessClaude, session.StatusProvisioning, 1000, 0, "example.test")
	stopped, stoppedID := st.add(session.HarnessClaude, session.StatusStopped, 1000, 0, "example.test")

	resp, _, _ := connect(t, e.proxy.URL, "CONNECT", "example.test:443", "", "")
	if resp.StatusCode != http.StatusProxyAuthRequired || !strings.HasPrefix(resp.Header.Get("Proxy-Authenticate"), "Basic") {
		t.Fatalf("missing auth: %d %v", resp.StatusCode, resp.Header)
	}
	for _, c := range []struct {
		name, target, auth string
		status             int
	}{
		{"unknown secret", "example.test:443", basic(credentials.NewSecret()), 403},
		{"wrong user", "example.test:443", "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+secret)), 403},
		{"host not allowed", "github.com:443", basic(secret), 403},
		{"suffix trick", "example.test.evil.com:443", basic(secret), 403},
		{"wrong port", "example.test:8443", basic(secret), 403},
		{"no port", "example.test", basic(secret), 403},
		{"stopped session", "example.test:443", basic(stopped), 403},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, _, _ := connect(t, e.proxy.URL, "CONNECT", c.target, c.auth, "")
			if resp.StatusCode != c.status {
				t.Fatalf("status %d", resp.StatusCode)
			}
		})
	}
	resp, _, _ = connect(t, e.proxy.URL, "GET", "http://plain.test/", basic(secret), "")
	if resp.StatusCode != 403 {
		t.Fatalf("plain GET: %d", resp.StatusCode)
	}

	resp, c, br := connect(t, e.proxy.URL, "CONNECT", "example.test:443", basic(secret), "early-bytes|")
	if resp.StatusCode != 200 {
		t.Fatalf("allowed: %d", resp.StatusCode)
	}
	io.WriteString(c, "more")
	buf := make([]byte, len("early-bytes|more"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "early-bytes|more" {
		t.Fatalf("tunnel echoed %q, %v", buf, err)
	}

	got := map[string]string{}
	for _, ev := range st.eventsFor(id) {
		if ev["via"] != "connect" {
			t.Errorf("event %v", ev)
		}
		got[ev["host"].(string)] = fmt.Sprint(ev["decision"], "/", ev["reason"])
	}
	want := map[string]string{
		"github.com:443":            "denied/host",
		"example.test.evil.com:443": "denied/host",
		"example.test:8443":         "denied/port",
		"example.test":              "denied/port",
		"plain.test":                "denied/method",
		"example.test:443":          "allowed/<nil>",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("event for %s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if ev := st.eventsFor(stoppedID); len(ev) != 1 || ev[0]["reason"] != "status" {
		t.Fatalf("stopped events %v", ev)
	}
}

func TestUsageTeeMalformed(t *testing.T) {
	for _, body := range []string{"", "{", "null", `{"usage":"x"}`, `{"usage":{"input_tokens":"a"}}`, "\x00\xff"} {
		var reported int64
		tee := newUsageTee(io.NopCloser(strings.NewReader(body)), usageClaude, "application/json", func(n int64) { reported = n })
		b, err := io.ReadAll(tee)
		if err != nil || string(b) != body {
			t.Fatalf("body altered: %q %v", b, err)
		}
		tee.Close()
		if reported != 0 {
			t.Fatalf("reported %d for %q", reported, body)
		}
	}
	var calls int
	tee := newUsageTee(io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":2,"output_tokens":3}}`)), usageClaude, "application/json", func(int64) { calls++ })
	io.ReadAll(tee)
	tee.Close()
	tee.Close()
	if calls != 1 {
		t.Fatalf("reported %d times", calls)
	}
}
