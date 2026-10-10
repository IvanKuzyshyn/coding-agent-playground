package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

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
	g := NewWithT(t)
	f := newFixture(t)
	g.Expect(sandboxclient.New(f.url, "", nil).Health(context.Background())).To(Succeed())
}

func TestAuthRequired(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	// Cases are identified by index so a failure never prints the token.
	for i, auth := range []string{"", "Bearer wrong", "Bearer " + testToken + "x", testToken} {
		req, _ := http.NewRequest(http.MethodGet, f.url+"/commands/x", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		g.Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		g.Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized), "auth case %d", i)
	}
	bad := sandboxclient.New(f.url, "wrong", nil)
	_, err := bad.StartCommand(context.Background(), sandboxclient.CommandRequest{ID: "a", Argv: []string{"true"}})
	g.Expect(err).To(MatchError(ContainSubstring("401")), "start with wrong token")
}

func TestEnvExcludesToken(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID: "env", Argv: []string{"env"}, Env: map[string]string{"EXTRA": "yes"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	f.waitExit(t, "env")
	out := f.readAll(t, "env", "stdout")
	// Assert on booleans so a failure does not print the env (and with it the token).
	g.Expect(strings.Contains(out, tokenEnv)).To(BeFalse(), "child env leaks token variable")
	g.Expect(strings.Contains(out, testToken)).To(BeFalse(), "child env leaks token")
	g.Expect(out).To(ContainSubstring("EXTRA=yes\n"), "request env missing")
	g.Expect(out).To(ContainSubstring("PATH="), "base env missing")
}

func TestExitCodesAndCwd(t *testing.T) {
	g := NewWithT(t)
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
		_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: c.id, Argv: c.argv, Cwd: c.cwd})
		g.Expect(err).NotTo(HaveOccurred(), c.id)
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			NewWithT(t).Expect(exitCodeOf(t, f.waitExit(t, c.id))).To(Equal(c.code), "exit code")
		})
	}
	g.Expect(f.readAll(t, "missing", "stderr")).To(ContainSubstring("sandboxd: start"), "missing binary stderr")
	wd, _ := filepath.EvalSymlinks(filepath.Join(f.home, "work"))
	g.Expect(strings.TrimSpace(f.readAll(t, "cwd", "stdout"))).To(BeElementOf(wd, filepath.Join(f.home, "work")), "default cwd")
}

func TestStartIdempotentConcurrent(t *testing.T) {
	g := NewWithT(t)
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
		g.Expect(s).To(BeTemporally("==", started[0]), "different started_at across duplicate starts")
	}
	f.waitExit(t, "once")
	// A start after exit returns the exited status and runs nothing.
	c, err := f.client.StartCommand(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(c.State).To(Equal(sandboxclient.StateExited), "restart state")
	data, err := os.ReadFile(marker)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(strings.Count(string(data), "started")).To(Equal(1), "command run count")
}

func TestLogOffsetsWhileRunning(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID:   "stream",
		Argv: []string{"sh", "-c", `for i in 1 2 3 4 5; do echo "line $i"; echo "err $i" >&2; sleep 0.1; done`},
	})
	g.Expect(err).NotTo(HaveOccurred())
	// Wait for partial output while still running, then continue from that offset.
	var first []byte
	var offset int64
	for {
		data, next, state, err := f.client.Logs(ctx, "stream", "stdout", 0)
		g.Expect(err).NotTo(HaveOccurred())
		if len(data) > 0 {
			g.Expect(state).To(Equal(sandboxclient.StateRunning), "expected running with partial output")
			first, offset = data, next
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var rest bytes.Buffer
	for {
		data, next, state, err := f.client.Logs(ctx, "stream", "stdout", offset)
		g.Expect(err).NotTo(HaveOccurred())
		rest.Write(data)
		offset = next
		if len(data) == 0 && state == sandboxclient.StateExited {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	g.Expect(string(first)+rest.String()).To(Equal(want), "stdout")
	g.Expect(f.readAll(t, "stream", "stderr")).To(Equal(strings.ReplaceAll(want, "line", "err")), "stderr")
	_, _, _, err = f.client.Logs(ctx, "nope", "stdout", 0)
	g.Expect(err).To(MatchError(sandboxclient.ErrNotFound), "logs of unknown command")
}

func TestLargeOutputChunked(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	const size = 3<<20 + 123
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{
		ID: "big", Argv: []string{"sh", "-c", "head -c 3145851 /dev/zero"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	f.waitExit(t, "big")
	data, next, _, err := f.client.Logs(ctx, "big", "stdout", 0)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(data).To(HaveLen(maxLogChunk), "first chunk")
	g.Expect(next).To(BeEquivalentTo(maxLogChunk), "first chunk next offset")
	g.Expect(f.readAll(t, "big", "stdout")).To(HaveLen(size), "total")
}

func TestSignalProcessGroup(t *testing.T) {
	g := NewWithT(t)
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
	g.Expect(err).NotTo(HaveOccurred())
	for {
		if b, _ := os.ReadFile(pidFile); len(b) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.Expect(f.client.Signal(ctx, "sleepy", "TERM")).To(Succeed())
	c := f.waitExit(t, "sleepy")
	g.Expect(exitCodeOf(t, c)).To(Equal(0), "shell survives, sleep killed")
	g.Expect(f.readAll(t, "sleepy", "stdout")).To(Equal("after-wait\n"), "stdout")
	g.Expect(f.client.Signal(ctx, "sleepy", "TERM")).To(MatchError(sandboxclient.ErrExited), "signal after exit")
	g.Expect(f.client.Signal(ctx, "nope", "TERM")).To(MatchError(sandboxclient.ErrNotFound), "signal unknown")
}

func TestSignalKillReportsSignal(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "k", Argv: []string{"sleep", "30"}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(f.client.Signal(ctx, "k", "KILL")).To(Succeed())
	g.Expect(exitCodeOf(t, f.waitExit(t, "k"))).To(Equal(128+9), "exit code")
	g.Expect(f.client.Signal(ctx, "k", "HUP")).To(MatchError(ContainSubstring("400")), "bad signal name")
}

func TestTerminateAll(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "s", Argv: []string{"sleep", "30"}})
	g.Expect(err).NotTo(HaveOccurred())
	start := time.Now()
	f.srv.terminateAll(5 * time.Second)
	g.Expect(time.Since(start)).To(BeNumerically("<=", 3*time.Second), "terminateAll waited too long")
	g.Expect(exitCodeOf(t, f.waitExit(t, "s"))).To(Equal(128+15), "exit code")
	_, err = f.client.StartCommand(ctx, sandboxclient.CommandRequest{ID: "late", Argv: []string{"true"}})
	g.Expect(err).To(MatchError(ContainSubstring("503")), "start after shutdown")
}

func TestFiles(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	path := filepath.Join(f.home, "a", "b", "c.txt")
	g.Expect(f.client.PutFile(ctx, path, []byte("hello"), 0o600)).To(Succeed())
	got, err := f.client.GetFile(ctx, path)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(got)).To(Equal("hello"))
	st, err := os.Stat(path)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o600)), "mode")
	g.Expect(f.client.PutFile(ctx, path, []byte("bye"), 0o755)).To(Succeed())
	got, _ = f.client.GetFile(ctx, path)
	g.Expect(string(got)).To(Equal("bye"), "overwrite")
	entries, _ := os.ReadDir(filepath.Dir(path))
	g.Expect(entries).To(HaveLen(1), "temp files left behind")
	_, err = f.client.GetFile(ctx, filepath.Join(f.home, "missing"))
	g.Expect(err).To(MatchError(sandboxclient.ErrNotFound), "missing file")
}

func TestPathAndRequestValidation(t *testing.T) {
	g := NewWithT(t)
	f := newFixture(t)
	ctx := context.Background()
	for _, p := range []string{"", "relative/x", f.home + "/a/../b", f.home + "/a/", f.home + "//a"} {
		g.Expect(f.client.PutFile(ctx, p, []byte("x"), 0o644)).To(MatchError(ContainSubstring("400")), "PutFile(%q)", p)
		_, err := f.client.GetFile(ctx, p)
		g.Expect(err).To(MatchError(ContainSubstring("400")), "GetFile(%q)", p)
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
		_, err := f.client.StartCommand(ctx, r)
		g.Expect(err).To(MatchError(ContainSubstring("400")), "StartCommand(%+v)", r)
	}
	_, err := f.client.Command(ctx, "unknown")
	g.Expect(err).To(MatchError(sandboxclient.ErrNotFound), "unknown command")
}
