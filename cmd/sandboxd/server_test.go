package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terrarium/internal/sandboxclient"
)

const testToken = "test-token"

type fixture struct {
	srv    *server
	client *sandboxclient.Client
	url    string
	home   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	stateDir, workDir := filepath.Join(home, ".sandboxd"), filepath.Join(home, "work")
	for _, d := range []string{stateDir, workDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tokenEnv, testToken)
	tok := takeToken()
	if tok != testToken {
		t.Fatalf("takeToken = %q", tok)
	}
	s := newServer(tok, stateDir, workDir)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(func() {
		s.terminateAll(5 * time.Second)
		ts.Close()
	})
	return &fixture{srv: s, client: sandboxclient.New(ts.URL, tok, nil), url: ts.URL, home: home}
}

// waitExit polls until the command exits and returns it.
func (f *fixture) waitExit(t *testing.T, id string) sandboxclient.Command {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := f.client.Command(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if c.State == sandboxclient.StateExited {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("command %s did not exit", id)
	return sandboxclient.Command{}
}

// readAll follows a stream using the contract's termination rule.
func (f *fixture) readAll(t *testing.T, id, stream string) string {
	t.Helper()
	var out bytes.Buffer
	var offset int64
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, next, state, err := f.client.Logs(context.Background(), id, stream, offset)
		if err != nil {
			t.Fatal(err)
		}
		if next != offset+int64(len(data)) {
			t.Fatalf("next = %d, want %d", next, offset+int64(len(data)))
		}
		out.Write(data)
		offset = next
		if len(data) == 0 {
			if state == sandboxclient.StateExited {
				return out.String()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatalf("stream %s of %s not finished", stream, id)
	return ""
}

func exitCodeOf(t *testing.T, c sandboxclient.Command) int {
	t.Helper()
	if c.ExitCode == nil || c.EndedAt == nil {
		t.Fatalf("exited command without exit_code/ended_at: %+v", c)
	}
	return *c.ExitCode
}

func TestHealthNoAuth(t *testing.T) {
	f := newFixture(t)
	if err := sandboxclient.New(f.url, "", nil).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAuthRequired(t *testing.T) {
	f := newFixture(t)
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + testToken + "x", testToken} {
		req, _ := http.NewRequest(http.MethodGet, f.url+"/commands/x", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d, want 401", auth, resp.StatusCode)
		}
	}
	bad := sandboxclient.New(f.url, "wrong", nil)
	if _, err := bad.StartCommand(context.Background(), sandboxclient.CommandRequest{ID: "a", Argv: []string{"true"}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("start with wrong token: %v", err)
	}
}

func TestEnvExcludesToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID: "env", Argv: []string{"env"}, Env: map[string]string{"EXTRA": "yes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.waitExit(t, "env")
	out := f.readAll(t, "env", "stdout")
	if strings.Contains(out, tokenEnv) || strings.Contains(out, testToken) {
		t.Errorf("child env leaks token:\n%s", out)
	}
	if !strings.Contains(out, "EXTRA=yes\n") {
		t.Errorf("request env missing:\n%s", out)
	}
	if !strings.Contains(out, "PATH=") {
		t.Errorf("base env missing:\n%s", out)
	}
}

func TestExitCodesAndCwd(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cases := []struct {
		id   string
		argv []string
		cwd  string
		code int
	}{
		{"ok", []string{"true"}, "", 0},
		{"fail", []string{"sh", "-c", "exit 3"}, "", 3},
		{"missing", []string{"/no/such/binary"}, "", startFailedCode},
		{"badcwd", []string{"true"}, "/no/such/dir", startFailedCode},
		{"cwd", []string{"pwd"}, "", 0},
	}
	for _, c := range cases {
		if _, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: c.id, Argv: c.argv, Cwd: c.cwd}); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
	}
	for _, c := range cases {
		if got := exitCodeOf(t, f.waitExit(t, c.id)); got != c.code {
			t.Errorf("%s: exit code %d, want %d", c.id, got, c.code)
		}
	}
	if got := f.readAll(t, "missing", "stderr"); !strings.Contains(got, "sandboxd: start") {
		t.Errorf("missing binary stderr = %q", got)
	}
	wd, _ := filepath.EvalSymlinks(filepath.Join(f.home, "work"))
	if got := strings.TrimSpace(f.readAll(t, "cwd", "stdout")); got != wd && got != filepath.Join(f.home, "work") {
		t.Errorf("default cwd = %q, want %q", got, wd)
	}
}

func TestStartIdempotentConcurrent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	marker := filepath.Join(f.home, "marker")
	req := sandboxclient.CommandRequest{
		ID:   "once",
		Argv: []string{"sh", "-c", `echo started >> "$MARKER"; sleep 0.3`},
		Env:  map[string]string{"MARKER": marker},
	}
	var wg sync.WaitGroup
	started := make([]time.Time, 20)
	for i := range started {
		wg.Go(func() {
			c, err := f.client.StartCommand(ctx, req)
			if err != nil {
				t.Error(err)
				return
			}
			started[i] = c.StartedAt
		})
	}
	wg.Wait()
	for _, s := range started[1:] {
		if !s.Equal(started[0]) {
			t.Fatalf("different started_at across duplicate starts: %v vs %v", s, started[0])
		}
	}
	f.waitExit(t, "once")
	// A start after exit returns the exited status and runs nothing.
	c, err := f.client.StartCommand(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != sandboxclient.StateExited {
		t.Errorf("restart state = %s", c.State)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "started"); n != 1 {
		t.Errorf("command ran %d times", n)
	}
}

func TestLogOffsetsWhileRunning(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID:   "stream",
		Argv: []string{"sh", "-c", `for i in 1 2 3 4 5; do echo "line $i"; echo "err $i" >&2; sleep 0.1; done`},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait for partial output while still running, then continue from that offset.
	var first []byte
	var offset int64
	for {
		data, next, state, err := f.client.Logs(ctx, "stream", "stdout", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 {
			if state != sandboxclient.StateRunning {
				t.Fatalf("expected running with partial output, got %s", state)
			}
			first, offset = data, next
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var rest bytes.Buffer
	for {
		data, next, state, err := f.client.Logs(ctx, "stream", "stdout", offset)
		if err != nil {
			t.Fatal(err)
		}
		rest.Write(data)
		offset = next
		if len(data) == 0 && state == sandboxclient.StateExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if got := string(first) + rest.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got := f.readAll(t, "stream", "stderr"); got != strings.ReplaceAll(want, "line", "err") {
		t.Errorf("stderr = %q", got)
	}
	if _, _, _, err := f.client.Logs(ctx, "nope", "stdout", 0); !errors.Is(err, sandboxclient.ErrNotFound) {
		t.Errorf("logs of unknown command: %v", err)
	}
}

func TestLargeOutputChunked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const size = 3<<20 + 123
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID: "big", Argv: []string{"sh", "-c", "head -c 3145851 /dev/zero"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.waitExit(t, "big")
	data, next, _, err := f.client.Logs(ctx, "big", "stdout", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxLogChunk || next != maxLogChunk {
		t.Errorf("first chunk %d bytes, next %d", len(data), next)
	}
	if got := len(f.readAll(t, "big", "stdout")); got != size {
		t.Errorf("total %d, want %d", got, size)
	}
}

func TestSignalProcessGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pidFile := filepath.Join(f.home, "child.pid")
	// The shell ignores TERM (set after forking sleep, since ignored signals are inherited), so
	// the command only exits if TERM reaches the sleep in its group.
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID:   "sleepy",
		Argv: []string{"sh", "-c", `sleep 30 & trap '' TERM; echo $! > "$PIDFILE"; wait; echo after-wait`},
		Env:  map[string]string{"PIDFILE": pidFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if b, _ := os.ReadFile(pidFile); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := f.client.Signal(ctx, "sleepy", "TERM"); err != nil {
		t.Fatal(err)
	}
	c := f.waitExit(t, "sleepy")
	if got := exitCodeOf(t, c); got != 0 {
		t.Errorf("exit code %d, want 0 (shell survives, sleep killed)", got)
	}
	if out := f.readAll(t, "sleepy", "stdout"); out != "after-wait\n" {
		t.Errorf("stdout = %q", out)
	}
	if err := f.client.Signal(ctx, "sleepy", "TERM"); !errors.Is(err, sandboxclient.ErrExited) {
		t.Errorf("signal after exit: %v", err)
	}
	if err := f.client.Signal(ctx, "nope", "TERM"); !errors.Is(err, sandboxclient.ErrNotFound) {
		t.Errorf("signal unknown: %v", err)
	}
}

func TestSignalKillReportsSignal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "k", Argv: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Signal(ctx, "k", "KILL"); err != nil {
		t.Fatal(err)
	}
	if got := exitCodeOf(t, f.waitExit(t, "k")); got != 128+9 {
		t.Errorf("exit code %d, want 137", got)
	}
	if err := f.client.Signal(ctx, "k", "HUP"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("bad signal name: %v", err)
	}
}

func TestTerminateAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "s", Argv: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.srv.terminateAll(5 * time.Second)
	if time.Since(start) > 3*time.Second {
		t.Error("terminateAll waited too long")
	}
	if got := exitCodeOf(t, f.waitExit(t, "s")); got != 128+15 {
		t.Errorf("exit code %d, want 143", got)
	}
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "late", Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("start after shutdown: %v", err)
	}
}

func TestFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := filepath.Join(f.home, "a", "b", "c.txt")
	if err := f.client.PutFile(ctx, path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := f.client.GetFile(ctx, path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("GetFile = %q, %v", got, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if err := f.client.PutFile(ctx, path, []byte("bye"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.client.GetFile(ctx, path); string(got) != "bye" {
		t.Errorf("overwrite = %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	if _, err := f.client.GetFile(ctx, filepath.Join(f.home, "missing")); !errors.Is(err, sandboxclient.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
}

func TestPathAndRequestValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, p := range []string{"", "relative/x", f.home + "/a/../b", f.home + "/a/", f.home + "//a"} {
		if err := f.client.PutFile(ctx, p, []byte("x"), 0o644); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("PutFile(%q): %v", p, err)
		}
		if _, err := f.client.GetFile(ctx, p); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("GetFile(%q): %v", p, err)
		}
	}
	bad := []sandboxclient.CommandRequest{
		{ID: "", Argv: []string{"true"}},
		{ID: "has space", Argv: []string{"true"}},
		{ID: "../x", Argv: []string{"true"}},
		{ID: strings.Repeat("a", 129), Argv: []string{"true"}},
		{ID: "noargv"},
		{ID: "relcwd", Argv: []string{"true"}, Cwd: "work"},
		{ID: "badenv", Argv: []string{"true"}, Env: map[string]string{"A=B": "c"}},
	}
	for _, r := range bad {
		if _, err := f.client.StartCommand(ctx, r); err == nil || !strings.Contains(err.Error(), "400") {
			t.Errorf("StartCommand(%+v): %v", r, err)
		}
	}
	if _, err := f.client.Command(ctx, "unknown"); !errors.Is(err, sandboxclient.ErrNotFound) {
		t.Errorf("unknown command: %v", err)
	}
}
