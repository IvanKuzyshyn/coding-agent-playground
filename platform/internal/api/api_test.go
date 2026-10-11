package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"

	"terrarium/internal/api"
	"terrarium/internal/catalog"
	"terrarium/internal/credentials"
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

func newEnv(t *testing.T, eventPoll time.Duration) env {
	t.Helper()
	st := testutil.NewStore(t)
	cat, err := catalog.Load(filepath.Join("..", "..", "config", "presets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"alice": alice, "bob": bob} {
		if _, err := st.EnsureUser(context.Background(), name, credentials.Hash(token)); err != nil {
			t.Fatal(err)
		}
	}
	s := &api.Server{
		Store:      st,
		Catalog:    cat,
		LoginHosts: []string{"localhost"},
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := specClient(t).Do(req)
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
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/sessions", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			g.Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			g.Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	}
	g := NewWithT(t)
	code, body := e.do(t, http.MethodGet, "/whoami", alice, nil)
	g.Expect(code).To(Equal(http.StatusOK), "whoami: %v", body)
	g.Expect(body).To(HaveKeyWithValue("name", "alice"), "whoami")
	code, _ = e.do(t, http.MethodGet, "/healthz", "", nil)
	g.Expect(code).To(Equal(http.StatusOK), "healthz without auth")
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
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.do(t, http.MethodPost, "/sessions", alice, tc.body)
			NewWithT(t).Expect(code).To(Equal(http.StatusBadRequest), "%v", body)
		})
	}
	g := NewWithT(t)
	code, body := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": "test run", "harness": "codex", "prompt": "hi", "repo": "octo/hello.go", "preset": "explore"})
	g.Expect(code).To(Equal(http.StatusAccepted), "create: %v", body)
	g.Expect(body).To(And(
		HaveKeyWithValue("status", "pending"),
		HaveKeyWithValue("owner", "alice"),
		HaveKeyWithValue("preset", "explore"),
		HaveKeyWithValue("repo", "octo/hello.go"),
	), "create")
	s := testutil.MustSession(t, e.st, uuid.MustParse(body["id"].(string)))
	g.Expect(s.Spec.Limits.TokenBudget).To(BeEquivalentTo(500000), "spec not frozen from preset: %+v", s.Spec)
	g.Expect(s.Spec.Harness).To(Equal(session.HarnessCodex), "spec not frozen from preset: %+v", s.Spec)
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Prompt).To(Equal("hi"), "turn 1: %+v", tn)
	g.Expect(tn.Status).To(Equal(session.TurnQueued), "turn 1: %+v", tn)
}

// Everyone can read every session; only the user who started one can change it.
func TestOtherUsersSessionIsReadOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	p := "/sessions/" + id.String()
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, p, nil, http.StatusOK},
		{http.MethodGet, p + "/events", nil, http.StatusOK},
		{http.MethodDelete, p, nil, http.StatusForbidden},
		{http.MethodPost, p + "/turns", map[string]string{"prompt": "x"}, http.StatusForbidden},
		{http.MethodPost, p + "/turns/current/stop", nil, http.StatusForbidden},
	} {
		t.Run(tc.method+" "+strings.TrimPrefix(tc.path, p), func(t *testing.T) {
			code, _ := e.do(t, tc.method, tc.path, bob, tc.body)
			NewWithT(t).Expect(code).To(Equal(tc.want), "bob %s %s", tc.method, tc.path)
		})
	}
	g := NewWithT(t)
	code, _ := e.do(t, http.MethodGet, "/sessions/not-a-uuid", alice, nil)
	g.Expect(code).To(Equal(http.StatusNotFound), "bad id")
	code, _ = e.do(t, http.MethodGet, "/sessions/"+uuid.NewString(), alice, nil)
	g.Expect(code).To(Equal(http.StatusNotFound), "unknown id")
	code, body := e.do(t, http.MethodGet, "/sessions", bob, nil)
	g.Expect(code).To(Equal(http.StatusOK), "bob's list: %v", body)
	g.Expect(body["sessions"]).To(HaveLen(1), "bob's list should include alice's session")
	_, body = e.do(t, http.MethodGet, "/sessions?owner=bob", bob, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(0)), "owner filter")
	g.Expect(testutil.MustSession(t, e.st, id).Status).To(Equal(session.StatusPending), "bob's DELETE changed alice's session")
	code, body = e.do(t, http.MethodGet, p, alice, nil)
	g.Expect(code).To(Equal(http.StatusOK), "owner get: %v", body)
	g.Expect(body["turns"]).To(HaveLen(1), "owner get")
}

func TestActiveSessionCap(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	first := e.create(t, alice)
	e.create(t, alice)
	code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": "test run", "harness": "codex", "prompt": "hi"})
	g.Expect(code).To(Equal(http.StatusTooManyRequests), "third session")
	e.create(t, bob) // the cap is per engineer
	e.stop(t, first)
	e.create(t, alice)
}

func TestTurnsAndStop(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	p := "/sessions/" + id.String()

	code, body := e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "next"})
	g.Expect(code).To(Equal(http.StatusAccepted), "follow-up on pending session: %v", body)
	g.Expect(body).To(And(HaveKeyWithValue("seq", 2.0), HaveKeyWithValue("status", "queued")), "follow-up on pending session")
	code, _ = e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": ""})
	g.Expect(code).To(Equal(http.StatusBadRequest), "empty follow-up")
	code, _ = e.do(t, http.MethodPost, p+"/turns/current/stop", alice, nil)
	g.Expect(code).To(Equal(http.StatusConflict), "stop without a running turn")
	_, err := e.st.StartTurn(context.Background(), id, 1, "turn-1")
	g.Expect(err).NotTo(HaveOccurred())
	code, _ = e.do(t, http.MethodPost, p+"/turns/current/stop", alice, nil)
	g.Expect(code).To(Equal(http.StatusAccepted), "stop running turn")
	g.Expect(testutil.MustTurn(t, e.st, id, 1).StopRequested).To(BeTrue(), "stop not recorded")

	code, body = e.do(t, http.MethodDelete, p, alice, nil)
	g.Expect(code).To(Equal(http.StatusAccepted), "delete: %v", body)
	g.Expect(body).To(HaveKeyWithValue("status", "stopping"), "delete")
	code, _ = e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "late"})
	g.Expect(code).To(Equal(http.StatusConflict), "follow-up on stopping session")
	e.setStatus(t, id, []string{session.StatusStopping}, session.StatusStopped)
	code, _ = e.do(t, http.MethodPost, p+"/turns", alice, map[string]string{"prompt": "late"})
	g.Expect(code).To(Equal(http.StatusConflict), "follow-up on stopped session")
}

func TestEventsPaging(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	seq := 1
	for i := range 4 {
		err := e.st.AppendEvent(context.Background(), id, store.EventStderr, &seq, map[string]string{"line": strconv.Itoa(i)})
		g.Expect(err).NotTo(HaveOccurred())
	}
	p := "/sessions/" + id.String() + "/events"

	var ids []float64
	after := 0.0
	for range 3 {
		code, body := e.do(t, http.MethodGet, p+"?limit=2&after="+strconv.FormatFloat(after, 'f', 0, 64), alice, nil)
		g.Expect(code).To(Equal(http.StatusOK), "page: %v", body)
		for _, ev := range body["events"].([]any) {
			ids = append(ids, ev.(map[string]any)["id"].(float64))
		}
		after = body["next_after"].(float64)
	}
	g.Expect(ids).To(HaveLen(5), "paged events, want created + 4 stderr")
	for i := 1; i < len(ids); i++ {
		g.Expect(ids[i]).To(BeNumerically(">", ids[i-1]), "ids not increasing: %v", ids)
	}
	g.Expect(after).To(Equal(ids[4]), "next_after")
	code, body := e.do(t, http.MethodGet, p+"?after="+strconv.FormatFloat(after, 'f', 0, 64), alice, nil)
	g.Expect(code).To(Equal(http.StatusOK), "past the end: %v", body)
	g.Expect(body["events"]).To(BeEmpty(), "past the end")
	g.Expect(body).To(HaveKeyWithValue("next_after", after), "past the end")
	code, body = e.do(t, http.MethodGet, p+"?kind=stderr", alice, nil)
	g.Expect(code).To(Equal(http.StatusOK), "kind filter: %v", body)
	g.Expect(body["events"]).To(HaveLen(4), "kind filter")
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
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := e.openSSE(t, ctx, id, "")
	created := next(t, ch)
	g.Expect(created.kind).To(Equal(store.EventLifecycle), "first event: %+v", created)
	g.Expect(created.data.ID).To(Equal(created.id), "first event: %+v", created)
	g.Expect(created.data.SessionID).To(Equal(id), "first event: %+v", created)
	seq := 1
	err := e.st.AppendEvent(context.Background(), id, store.EventHarness, &seq, map[string]string{"n": "1"})
	g.Expect(err).NotTo(HaveOccurred())
	live := next(t, ch)
	g.Expect(live.kind).To(Equal(store.EventHarness), "live event: %+v", live)
	g.Expect(live.id).To(BeNumerically(">", created.id), "live event: %+v", live)
	g.Expect(*live.data.TurnSeq).To(Equal(1), "live event: %+v", live)

	// Resume after the created event: only the later one is replayed.
	resumed := e.openSSE(t, ctx, id, strconv.FormatInt(created.id, 10))
	g.Expect(next(t, resumed).id).To(Equal(live.id), "Last-Event-ID resume")

	e.stop(t, id)
	var last sseEvent
	for _, c := range []<-chan sseEvent{ch, resumed} {
		var got []sseEvent
		timedOut := false
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
				timedOut = true
				break drain
			}
		}
		g.Expect(timedOut).To(BeFalse(), "stream did not end after the session stopped")
		g.Expect(got).NotTo(BeEmpty(), "no events after stop")
		last = got[len(got)-1]
		var p map[string]string
		_ = json.Unmarshal(last.data.Payload, &p)
		g.Expect(p).To(HaveKeyWithValue("status", session.StatusStopped), "last event before the stream ended: %s", last.data.Payload)
	}
}

// The stream may end between the event query and the status check; the terminal event committed
// in that window is never sent. EventPoll 0 makes the window a large share of each poll cycle.
func TestEventStreamDeliversTerminalEvent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
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
		g.Expect(p).To(HaveKeyWithValue("status", session.StatusStopped), "iteration %d: stream ended without the stopped event; last %s", i, last.data.Payload)
	}
}

func TestLogin(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	code, first := e.do(t, http.MethodPost, "/login", "", map[string]string{"name": "  Carol "})
	g.Expect(code).To(Equal(http.StatusOK), "first login: %v", first)
	g.Expect(first).To(And(HaveKeyWithValue("name", "carol"), HaveKeyWithValue("created", true)), "first login")
	g.Expect(first["token"]).To(HavePrefix("trm_"), "first login")
	_, again := e.do(t, http.MethodPost, "/login", "", map[string]string{"name": "carol"})
	g.Expect(again).To(And(HaveKeyWithValue("token", first["token"]), HaveKeyWithValue("created", false)), "second login")
	token := first["token"].(string)
	code, me := e.do(t, http.MethodGet, "/whoami", token, nil)
	g.Expect(code).To(Equal(http.StatusOK), "whoami with login token: %v", me)
	g.Expect(me).To(HaveKeyWithValue("name", "carol"), "whoami with login token")
	for _, name := range []string{"", "-x", "has space", "ünï", strings.Repeat("a", 40)} {
		t.Run(fmt.Sprintf("name %q", name), func(t *testing.T) {
			code, _ := e.do(t, http.MethodPost, "/login", "", map[string]string{"name": name})
			NewWithT(t).Expect(code).To(Equal(http.StatusBadRequest))
		})
	}
}

func TestLoginOnlyOnAllowedHosts(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	for host, want := range map[string]int{
		"":                      http.StatusOK, // Host is 127.0.0.1:<port>
		"localhost:3000":        http.StatusOK,
		"[::1]:3000":            http.StatusOK,
		"myhost.tailnet.ts.net": http.StatusForbidden,
		"192.168.1.10:3000":     http.StatusForbidden,
	} {
		t.Run(fmt.Sprintf("host %q", host), func(t *testing.T) {
			g := NewWithT(t)
			req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/login", strings.NewReader(`{"name":"dave"}`))
			if host != "" {
				req.Header.Set("X-Forwarded-Host", host)
			}
			resp, err := http.DefaultClient.Do(req)
			g.Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			g.Expect(resp.StatusCode).To(Equal(want))
		})
	}
}
