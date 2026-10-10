package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/api"
	"terrarium/internal/catalog"
	"terrarium/internal/session"
	"terrarium/internal/store"
	"terrarium/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

const (
	alice = "alice-token"
	bob   = "bob-token"
)

type env struct {
	st  *store.Store
	srv *httptest.Server
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newEnv(t *testing.T, eventPoll time.Duration) env {
	t.Helper()
	st := testutil.NewStore(t)
	cat, err := catalog.Load(filepath.Join("..", "..", "config", "presets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{
		Store:      st,
		Catalog:    cat,
		Engineers:  api.Engineers{hash(alice): "alice", hash(bob): "bob"},
		SessionKey: []byte("test-session-key"),
		MaxActive:  2,
		Log:        testutil.Logger(),
		EventPoll:  eventPoll,
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return env{st: st, srv: srv}
}

func (e env) do(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			r = strings.NewReader(s)
		} else {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
	}
	req, err := http.NewRequest(method, e.srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e env) create(t *testing.T, token string) uuid.UUID {
	t.Helper()
	code, body := e.do(t, http.MethodPost, "/sessions", token, map[string]string{"name": "test run", "harness": "claude-code", "prompt": "hi", "repo": "octo/hello"})
	if code != http.StatusAccepted {
		t.Fatalf("create: %d %v", code, body)
	}
	return uuid.MustParse(body["id"].(string))
}

func (e env) setStatus(t *testing.T, id uuid.UUID, from []string, to string) {
	t.Helper()
	if ok, err := e.st.SetStatus(context.Background(), id, from, to, "test"); err != nil || !ok {
		t.Fatalf("set status %s: %v %v", to, ok, err)
	}
}

func (e env) stop(t *testing.T, id uuid.UUID) {
	e.setStatus(t, id, []string{session.StatusPending}, session.StatusStopping)
	e.setStatus(t, id, []string{session.StatusStopping}, session.StatusStopped)
}

func TestAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	for _, tc := range []struct{ name, header string }{
		{"missing", ""},
		{"not bearer", "Basic " + alice},
		{"empty bearer", "Bearer "},
		{"unknown token", "Bearer nope"},
	} {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/sessions", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", tc.name, resp.StatusCode)
		}
	}
	if code, body := e.do(t, http.MethodGet, "/whoami", alice, nil); code != http.StatusOK || body["name"] != "alice" {
		t.Errorf("whoami: %d %v", code, body)
	}
	if code, _ := e.do(t, http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Errorf("healthz without auth: %d", code)
	}
}

func TestCreateValidates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	for _, tc := range []struct {
		name string
		body any
	}{
		{"unknown harness", map[string]string{"name": "test run", "harness": "gpt-shell", "prompt": "hi"}},
		{"missing harness", map[string]string{"prompt": "hi"}},
		{"empty prompt", map[string]string{"name": "test run", "harness": "codex", "prompt": "  "}},
		{"huge prompt", map[string]string{"name": "test run", "harness": "codex", "prompt": strings.Repeat("a", 100<<10+1)}},
		{"bad repo", map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "repo": "https://github.com/octo/hello"}},
		{"repo traversal", map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "repo": "octo/../x y"}},
		{"unknown preset", map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "preset": "nope"}},
		{"unknown field", map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "image": "evil"}},
		{"not json", "{"},
	} {
		if code, body := e.do(t, http.MethodPost, "/sessions", alice, tc.body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", tc.name, code, body)
		}
	}
	code, body := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "repo": "octo/hello.go", "preset": "explore"})
	if code != http.StatusAccepted || body["status"] != "pending" || body["owner"] != "alice" || body["preset"] != "explore" || body["repo"] != "octo/hello.go" {
		t.Fatalf("create: %d %v", code, body)
	}
	s := testutil.MustSession(t, e.st, uuid.MustParse(body["id"].(string)))
	if s.Spec.Limits.TokenBudget != 500000 || s.Spec.Harness != session.HarnessCodex {
		t.Errorf("spec not frozen from preset: %+v", s.Spec)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 1); tn.Prompt != "hi" || tn.Status != session.TurnQueued {
		t.Errorf("turn 1: %+v", tn)
	}
}

func TestOtherEngineersSessionIsNotFound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	p := "/sessions/" + id.String()
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, p, nil},
		{http.MethodDelete, p, nil},
		{http.MethodPost, p + "/turns", map[string]string{"prompt": "x"}},
		{http.MethodPost, p + "/turns/current/stop", nil},
		{http.MethodGet, p + "/events", nil},
	} {
		if code, _ := e.do(t, tc.method, tc.path, bob, tc.body); code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d", tc.method, tc.path, code)
		}
	}
	if code, _ := e.do(t, http.MethodGet, "/sessions/not-a-uuid", alice, nil); code != http.StatusNotFound {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/sessions/"+uuid.NewString(), alice, nil); code != http.StatusNotFound {
		t.Errorf("unknown id: %d", code)
	}
	if code, body := e.do(t, http.MethodGet, "/sessions", bob, nil); code != http.StatusOK || len(body["sessions"].([]any)) != 0 {
		t.Errorf("bob's list: %d %v", code, body)
	}
	if st := testutil.MustSession(t, e.st, id).Status; st != session.StatusPending {
		t.Errorf("bob's DELETE changed alice's session: %q", st)
	}
	code, body := e.do(t, http.MethodGet, p, alice, nil)
	if code != http.StatusOK || len(body["turns"].([]any)) != 1 {
		t.Errorf("owner get: %d %v", code, body)
	}
}

func TestActiveSessionCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	first := e.create(t, alice)
	e.create(t, alice)
	if code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": "test run", "harness": "codex", "prompt": "hi"}); code != http.StatusTooManyRequests {
		t.Errorf("third session: %d", code)
	}
	e.create(t, bob) // the cap is per engineer
	e.stop(t, first)
	e.create(t, alice)
}

func TestTurnsAndStop(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	p := "/sessions/" + id.String()

	if code, body := e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "next"}); code != http.StatusAccepted || body["seq"] != 2.0 || body["status"] != "queued" {
		t.Errorf("follow-up on pending session: %d %v", code, body)
	}
	if code, _ := e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": ""}); code != http.StatusBadRequest {
		t.Errorf("empty follow-up: %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, p+"/turns/current/stop", alice, nil); code != http.StatusConflict {
		t.Errorf("stop without a running turn: %d", code)
	}
	if _, err := e.st.StartTurn(context.Background(), id, 1, "turn-1"); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(t, http.MethodPost, p+"/turns/current/stop", alice, nil); code != http.StatusAccepted {
		t.Errorf("stop running turn: %d", code)
	}
	if !testutil.MustTurn(t, e.st, id, 1).StopRequested {
		t.Error("stop not recorded")
	}

	if code, body := e.do(t, http.MethodDelete, p, alice, nil); code != http.StatusAccepted || body["status"] != "stopping" {
		t.Errorf("delete: %d %v", code, body)
	}
	if code, _ := e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "late"}); code != http.StatusConflict {
		t.Errorf("follow-up on stopping session: %d", code)
	}
	e.setStatus(t, id, []string{session.StatusStopping}, session.StatusStopped)
	if code, _ := e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "late"}); code != http.StatusConflict {
		t.Errorf("follow-up on stopped session: %d", code)
	}
}

func TestEventsPaging(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	seq := 1
	for i := range 4 {
		if err := e.st.AppendEvent(context.Background(), id, store.EventStderr, &seq, map[string]string{"line": strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	p := "/sessions/" + id.String() + "/events"

	var ids []float64
	after := 0.0
	for range 3 {
		code, body := e.do(t, http.MethodGet, p+"?limit=2&after="+strconv.FormatFloat(after, 'f', 0, 64), alice, nil)
		if code != http.StatusOK {
			t.Fatalf("page: %d %v", code, body)
		}
		for _, ev := range body["events"].([]any) {
			ids = append(ids, ev.(map[string]any)["id"].(float64))
		}
		after = body["next_after"].(float64)
	}
	if len(ids) != 5 {
		t.Fatalf("paged %d events, want 5 (created + 4 stderr): %v", len(ids), ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("ids not increasing: %v", ids)
		}
	}
	if after != ids[4] {
		t.Errorf("next_after %v, want %v", after, ids[4])
	}
	code, body := e.do(t, http.MethodGet, p+"?after="+strconv.FormatFloat(after, 'f', 0, 64), alice, nil)
	if code != http.StatusOK || len(body["events"].([]any)) != 0 || body["next_after"] != after {
		t.Errorf("past the end: %d %v", code, body)
	}
	if code, body := e.do(t, http.MethodGet, p+"?kind=stderr", alice, nil); code != http.StatusOK || len(body["events"].([]any)) != 4 {
		t.Errorf("kind filter: %d %v", code, body)
	}
}

type sseEvent struct {
	id   int64
	kind string
	data store.Event
}

// openSSE starts an event stream and returns a channel of events that closes when the stream ends.
func (e env) openSSE(t *testing.T, ctx context.Context, id uuid.UUID, lastEventID string) <-chan sseEvent {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/sessions/"+id.String()+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+alice)
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("sse: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 100)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				ev.id, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				ev.kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.data)
			case line == "" && ev.id != 0:
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream ended early")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return sseEvent{}
}

func TestEventStream(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := e.openSSE(t, ctx, id, "")
	created := next(t, ch)
	if created.kind != store.EventLifecycle || created.data.ID != created.id || created.data.SessionID != id {
		t.Errorf("first event: %+v", created)
	}
	seq := 1
	if err := e.st.AppendEvent(context.Background(), id, store.EventHarness, &seq, map[string]string{"n": "1"}); err != nil {
		t.Fatal(err)
	}
	live := next(t, ch)
	if live.kind != store.EventHarness || live.id <= created.id || *live.data.TurnSeq != 1 {
		t.Errorf("live event: %+v", live)
	}

	// Resume after the created event: only the later one is replayed.
	resumed := e.openSSE(t, ctx, id, strconv.FormatInt(created.id, 10))
	if ev := next(t, resumed); ev.id != live.id {
		t.Errorf("Last-Event-ID resume returned %d, want %d", ev.id, live.id)
	}

	e.stop(t, id)
	var last sseEvent
	for _, c := range []<-chan sseEvent{ch, resumed} {
		var got []sseEvent
		timeout := time.After(5 * time.Second)
	drain:
		for {
			select {
			case ev, ok := <-c:
				if !ok {
					break drain
				}
				got = append(got, ev)
			case <-timeout:
				t.Fatal("stream did not end after the session stopped")
			}
		}
		if len(got) == 0 {
			t.Fatal("no events after stop")
		}
		last = got[len(got)-1]
		var p map[string]string
		_ = json.Unmarshal(last.data.Payload, &p)
		if p["status"] != session.StatusStopped {
			t.Errorf("last event before the stream ended: %s", last.data.Payload)
		}
	}
}

// The stream may end between the event query and the status check; the terminal event committed
// in that window is never sent. EventPoll 0 makes the window a large share of each poll cycle.
func TestEventStreamDeliversTerminalEvent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 0)
	for i := range 30 {
		id := e.create(t, alice)
		ctx, cancel := context.WithCancel(context.Background())
		ch := e.openSSE(t, ctx, id, "")
		next(t, ch)
		e.setStatus(t, id, []string{session.StatusPending}, session.StatusStopping)
		e.setStatus(t, id, []string{session.StatusStopping}, session.StatusStopped)
		var last sseEvent
		for ev := range ch {
			last = ev
		}
		cancel()
		var p map[string]string
		_ = json.Unmarshal(last.data.Payload, &p)
		if p["status"] != session.StatusStopped {
			t.Fatalf("iteration %d: stream ended without the stopped event; last %s", i, last.data.Payload)
		}
	}
}
