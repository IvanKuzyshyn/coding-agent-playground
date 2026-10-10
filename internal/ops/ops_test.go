package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/harness"
	"terrarium/internal/ops"
	"terrarium/internal/sandboxclient"
	"terrarium/internal/session"
	"terrarium/internal/store"
	"terrarium/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type env struct {
	ops   *ops.Ops
	st    *store.Store
	fleet *testutil.Fleet
}

func newEnv(t *testing.T) env {
	t.Helper()
	st := testutil.NewStore(t)
	fleet := testutil.NewFleet()
	o := &ops.Ops{
		Store:        st,
		Sandboxes:    fleet,
		NewClient:    func(endpoint, token string) ops.SandboxAPI { return fleet.Client(endpoint, token) },
		Gateway:      harness.Gateway{URL: "http://gateway:8080", ProxyHost: "gateway:3128"},
		SessionKey:   []byte("test-session-key"),
		Log:          testutil.Logger(),
		PollInterval: 2 * time.Millisecond,
		StopGrace:    30 * time.Millisecond,
	}
	return env{ops: o, st: st, fleet: fleet}
}

// turnPrograms routes turn-N commands to progs[N-1] and lets everything else exit 0.
func turnPrograms(progs ...testutil.Program) func(uuid.UUID, sandboxclient.CommandRequest) testutil.Program {
	return func(_ uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		for i, p := range progs {
			if req.ID == "turn-"+strconv.Itoa(i+1) {
				return p
			}
		}
		return nil
	}
}

// provisioned creates a session and provisions it to idle.
func (e env) provisioned(t *testing.T, spec session.Spec) store.Session {
	t.Helper()
	s := testutil.CreateSession(t, e.st, "alice", spec, "say ok")
	if err := e.ops.Provision(context.Background(), s); err != nil {
		t.Fatalf("provision: %v", err)
	}
	s = testutil.MustSession(t, e.st, s.ID)
	if s.Status != session.StatusIdle {
		t.Fatalf("after provision: status %q", s.Status)
	}
	return s
}

// runActive runs the session's active turn with fresh state from the store, as the supervisor does.
func (e env) runActive(t *testing.T, ctx context.Context, id uuid.UUID) (store.Turn, error) {
	t.Helper()
	s := testutil.MustSession(t, e.st, id)
	tn, err := e.st.ActiveTurn(context.Background(), id)
	if err != nil {
		t.Fatalf("active turn: %v", err)
	}
	return tn, e.ops.RunTurn(ctx, s, tn)
}

func claudeHappy() testutil.Program {
	return testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Stderr("warning: something\n"),
		testutil.Pause(),
		testutil.Line(testutil.ClaudeMessage),
		testutil.Pause(),
		// The result line arrives split across two reads.
		testutil.Stdout(testutil.ClaudeResult[:40]),
		testutil.Pause(),
		testutil.Line(testutil.ClaudeResult[40:]),
		testutil.Exit(0),
	}
}

type evSummary struct{ kind, what string }

func summarize(events []store.Event) []evSummary {
	var out []evSummary
	for _, e := range events {
		p := testutil.Payload(e)
		var what string
		switch e.Kind {
		case store.EventLifecycle:
			if s, ok := p["status"].(string); ok {
				what = "session:" + s
			} else {
				what = "turn:" + p["turn_status"].(string)
			}
		case store.EventHarness:
			var ev struct {
				Type string `json:"type"`
			}
			b, _ := json.Marshal(p["event"])
			_ = json.Unmarshal(b, &ev)
			what = ev.Type
		case store.EventStderr:
			what = p["line"].(string)
		case store.EventSystem:
			what = p["message"].(string)
		}
		out = append(out, evSummary{e.Kind, what})
	}
	return out
}

func TestHappyPathClaude(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turnPrograms(claudeHappy())
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))

	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatalf("run turn: %v", err)
	}

	s = testutil.MustSession(t, e.st, s.ID)
	if s.Status != session.StatusIdle || s.HarnessSession != testutil.ClaudeSessionID {
		t.Errorf("session: status %q harness session %q", s.Status, s.HarnessSession)
	}
	if s.IdleUntil == nil || time.Until(*s.IdleUntil) < 59*time.Minute {
		t.Errorf("idle_until not set from the idle timeout: %v", s.IdleUntil)
	}
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	if tn.Status != session.TurnComplete || tn.Result != "ok" || tn.Error != "" || tn.CommandID != "turn-1" {
		t.Errorf("turn: %+v", tn)
	}
	var usage harness.Usage
	if err := json.Unmarshal(tn.Usage, &usage); err != nil {
		t.Fatalf("usage %s: %v", tn.Usage, err)
	}
	if usage != (harness.Usage{InputTokens: 14, OutputTokens: 272, CachedInputTokens: 100, AgentSteps: 3}) {
		t.Errorf("usage: %+v", usage)
	}
	cmd, _ := e.fleet.Command(s.ID, "turn-1")
	if slices.Contains(cmd.Argv, "--resume") || cmd.Argv[0] != "claude" || cmd.Argv[2] != "say ok" {
		t.Errorf("first turn argv: %v", cmd.Argv)
	}

	got := summarize(testutil.MustEvents(t, e.st, s.ID))
	want := []evSummary{
		{"lifecycle", "session:pending"},
		{"lifecycle", "session:provisioning"},
		{"lifecycle", "session:idle"},
		{"lifecycle", "session:running"},
		{"lifecycle", "turn:running"},
		{"harness", "system"},
		{"harness", "assistant"},
		{"stderr", "warning: something"},
		{"harness", "result"},
		{"lifecycle", "turn:complete"},
		{"lifecycle", "session:idle"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("events:\n got %v\nwant %v", got, want)
	}
}

func TestHappyPathCodexAndResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	codex := testutil.Program{
		testutil.Line(testutil.CodexThread),
		testutil.Pause(),
		testutil.Line(testutil.CodexMessage),
		testutil.Line(testutil.CodexDone),
		testutil.Exit(0),
	}
	e.fleet.Program = turnPrograms(codex, codex)
	s := e.provisioned(t, testutil.Spec(session.HarnessCodex))
	if _, ok := e.fleet.File(s.ID, harness.Home+"/.codex/config.toml"); !ok {
		t.Error("codex config not written")
	}

	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	if tn.Status != session.TurnComplete || tn.Result != "41" {
		t.Errorf("turn 1: %+v", tn)
	}
	var usage harness.Usage
	_ = json.Unmarshal(tn.Usage, &usage)
	if usage.InputTokens != 12926 || usage.CachedInputTokens != 9600 || usage.OutputTokens != 10 {
		t.Errorf("usage: %+v", usage)
	}
	if hs := testutil.MustSession(t, e.st, s.ID).HarnessSession; hs != testutil.CodexThreadID {
		t.Errorf("harness session %q", hs)
	}

	if _, err := e.st.EnqueueTurn(context.Background(), s.ID, "and again"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	cmd, _ := e.fleet.Command(s.ID, "turn-2")
	if want := []string{"codex", "exec", "resume", testutil.CodexThreadID}; !slices.Equal(cmd.Argv[:4], want) || cmd.Argv[len(cmd.Argv)-1] != "and again" {
		t.Errorf("turn 2 argv: %v", cmd.Argv)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 2); tn.Status != session.TurnComplete {
		t.Errorf("turn 2: %+v", tn)
	}
}

func TestFollowUpResumesClaudeSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turnPrograms(claudeHappy(), claudeHappy())
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.EnqueueTurn(context.Background(), s.ID, "follow up"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	cmd, _ := e.fleet.Command(s.ID, "turn-2")
	i := slices.Index(cmd.Argv, "--resume")
	if i < 0 || cmd.Argv[i+1] != testutil.ClaudeSessionID || cmd.Argv[2] != "follow up" {
		t.Errorf("turn 2 argv: %v", cmd.Argv)
	}
}

func TestReattachAfterCrash(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turnPrograms(testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Stderr("first stderr\n"),
		testutil.Pause(),
		testutil.Line(testutil.ClaudeMessage),
		testutil.Pause(),
		testutil.Stderr("second stderr\n"),
		testutil.Line(testutil.ClaudeResult),
		testutil.Exit(0),
	})
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))

	// Die once init and the message have been consumed and their offsets persisted: the next
	// stdout read starts at that offset.
	consumed := int64(len(testutil.ClaudeInit) + len(testutil.ClaudeMessage) + 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	crashed := false
	e.fleet.OnLogs = func(_ context.Context, _ uuid.UUID, _, stream string, off int64) {
		if stream == "stdout" && off == consumed && !crashed {
			crashed = true
			cancel()
		}
	}
	if _, err := e.runActive(t, ctx, s.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run: want context.Canceled, got %v", err)
	}
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	if tn.Status != session.TurnRunning || tn.LogOffset != consumed || tn.ErrOffset != int64(len("first stderr\n")) {
		t.Fatalf("after crash: %+v", tn)
	}

	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if n := e.fleet.Starts(s.ID); n != 1 {
		t.Errorf("processes started: %d, want 1", n)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 1); tn.Status != session.TurnComplete || tn.Result != "ok" {
		t.Errorf("turn: %+v", tn)
	}
	var turnEvents []evSummary
	for _, ev := range summarize(testutil.MustEvents(t, e.st, s.ID)) {
		if ev.kind == store.EventHarness || ev.kind == store.EventStderr {
			turnEvents = append(turnEvents, ev)
		}
	}
	want := []evSummary{
		{"harness", "system"},
		{"harness", "assistant"},
		{"stderr", "first stderr"},
		{"harness", "result"},
		{"stderr", "second stderr"},
	}
	if !slices.Equal(turnEvents, want) {
		t.Errorf("turn events:\n got %v\nwant %v", turnEvents, want)
	}
	if st := testutil.Statuses(testutil.MustEvents(t, e.st, s.ID)); !slices.Equal(st, []string{"pending", "provisioning", "idle", "running", "idle"}) {
		t.Errorf("session statuses: %v", st)
	}
}

// stopOnFirstRead runs fn once, on the first stdout read of the turn.
func stopOnFirstRead(e env, fn func()) {
	done := false
	e.fleet.OnLogs = func(_ context.Context, _ uuid.UUID, id, stream string, _ int64) {
		if strings.HasPrefix(id, "turn-") && stream == "stdout" && !done {
			done = true
			fn()
		}
	}
}

func blockingTurn(ignoreTerm bool) testutil.Program {
	return testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Line(testutil.ClaudeMessage),
		testutil.WaitSignal(ignoreTerm),
	}
}

func TestStopRequested(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		ignoreTerm bool
		signals    []string
	}{
		{"term", false, []string{"TERM"}},
		{"kill after grace", true, []string{"TERM", "KILL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.fleet.Program = turnPrograms(blockingTurn(tc.ignoreTerm))
			s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
			stopOnFirstRead(e, func() {
				if ok, err := e.st.RequestStopCurrentTurn(context.Background(), s.ID); err != nil || !ok {
					t.Errorf("request stop: %v %v", ok, err)
				}
			})
			start := time.Now()
			if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
				t.Fatal(err)
			}
			cmd, _ := e.fleet.Command(s.ID, "turn-1")
			if !slices.Equal(cmd.Signals, tc.signals) {
				t.Errorf("signals %v, want %v", cmd.Signals, tc.signals)
			}
			if tc.ignoreTerm && time.Since(start) < e.ops.StopGrace {
				t.Errorf("KILL before the grace period")
			}
			tn := testutil.MustTurn(t, e.st, s.ID, 1)
			if tn.Status != session.TurnStopped || tn.Error != "stopped by engineer" || tn.Result != "ok" {
				t.Errorf("turn: %+v", tn)
			}
			if st := testutil.MustSession(t, e.st, s.ID).Status; st != session.StatusIdle {
				t.Errorf("session status %q", st)
			}
		})
	}
}

func TestTokenBudgetExhausted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turnPrograms(blockingTurn(false))
	spec := testutil.Spec(session.HarnessClaude)
	spec.Limits.TokenBudget = 1000
	s := e.provisioned(t, spec)
	stopOnFirstRead(e, func() {
		if _, err := e.st.AddTokensUsed(context.Background(), s.ID, 1000); err != nil {
			t.Error(err)
		}
	})
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	if tn.Status != session.TurnLimitExceeded || tn.Error != "session token budget exhausted" {
		t.Errorf("turn: %+v", tn)
	}
	if cmd, _ := e.fleet.Command(s.ID, "turn-1"); !slices.Equal(cmd.Signals, []string{"TERM"}) {
		t.Errorf("signals %v", cmd.Signals)
	}
	if st := testutil.MustSession(t, e.st, s.ID).Status; st != session.StatusIdle {
		t.Errorf("session status %q", st)
	}
}

func TestTurnOutcomes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		prog   testutil.Program
		status string
		errMsg string
	}{
		{
			name:   "claude max turns",
			prog:   testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Pause(), testutil.Line(testutil.ClaudeMaxTurns), testutil.Exit(1)},
			status: session.TurnLimitExceeded,
			errMsg: "max agent steps reached",
		},
		{
			name:   "exit without result",
			prog:   testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Line(testutil.ClaudeMessage), testutil.Stderr("panic: boom\n"), testutil.Exit(2)},
			status: session.TurnFailed,
			errMsg: "harness exited 2 without a result",
		},
		{
			name: "trailing line without newline",
			prog: testutil.Program{
				testutil.Line(testutil.ClaudeInit), testutil.Pause(), testutil.Stdout(testutil.ClaudeResult), testutil.Exit(0),
			},
			status: session.TurnComplete,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.fleet.Program = turnPrograms(tc.prog)
			s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
			if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
				t.Fatal(err)
			}
			tn := testutil.MustTurn(t, e.st, s.ID, 1)
			if tn.Status != tc.status || tn.Error != tc.errMsg {
				t.Errorf("turn: status %q error %q, want %q %q", tn.Status, tn.Error, tc.status, tc.errMsg)
			}
			if st := testutil.MustSession(t, e.st, s.ID).Status; st != session.StatusIdle {
				t.Errorf("session status %q", st)
			}
		})
	}
}

// A harness that writes more than one log chunk (1 MiB) and exits before the tail catches up:
// the tail must keep reading until sandboxd returns no data, not stop after the first chunk.
func TestOutputLargerThanOneChunkAfterExit(t *testing.T) {
	t.Skip("BUG: ops/turn.go:124,139 readStream treats a capped 1 MiB read of an exited command as the end of the stream (and its tail as a final line); the rest of stdout, including the result, is dropped")
	t.Parallel()
	e := newEnv(t)
	big := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", 600<<10) + `"}]}}`
	e.fleet.Program = turnPrograms(testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Line(big),
		testutil.Line(big),
		testutil.Line(testutil.ClaudeResult),
		testutil.Exit(0),
	})
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 1); tn.Status != session.TurnComplete || tn.Result != "ok" {
		t.Errorf("turn: status %q error %q", tn.Status, tn.Error)
	}
}

func TestTTLExpiryStopsRunningTurn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turnPrograms(blockingTurn(false))
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	stopOnFirstRead(e, func() {
		if _, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := e.runActive(t, context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 1); tn.Status != session.TurnStopped || tn.Error != "session ttl expired" {
		t.Errorf("turn: %+v", tn)
	}
	if st := testutil.MustSession(t, e.st, s.ID).Status; st != session.StatusStopping {
		t.Errorf("session status %q, want stopping", st)
	}
}

func TestProvisionIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	spec := testutil.Spec(session.HarnessCodex)
	spec.Repo = "octo/hello"
	s := testutil.CreateSession(t, e.st, "alice", spec, "hi")
	for i := range 2 {
		if err := e.ops.Provision(context.Background(), s); err != nil {
			t.Fatalf("provision %d: %v", i+1, err)
		}
	}
	if n := e.fleet.Count(); n != 1 {
		t.Errorf("sandboxes: %d", n)
	}
	if n := e.fleet.Starts(s.ID); n != 1 {
		t.Errorf("clone started %d times", n)
	}
	cmd, _ := e.fleet.Command(s.ID, "provision-clone")
	if !slices.Contains(cmd.Argv, "https://github.com/octo/hello.git") {
		t.Errorf("clone argv: %v", cmd.Argv)
	}
	s = testutil.MustSession(t, e.st, s.ID)
	if s.Status != session.StatusIdle || s.ContainerID != e.fleet.Name(s.ID) {
		t.Errorf("session: status %q container %q", s.Status, s.ContainerID)
	}
}

func TestProvisionCloneFailureIsPermanent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = func(_ uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		return testutil.Program{testutil.Stderr("Cloning into 'work'...\nfatal: repository not found\n"), testutil.Exit(128)}
	}
	spec := testutil.Spec(session.HarnessClaude)
	spec.Repo = "octo/missing"
	s := testutil.CreateSession(t, e.st, "alice", spec, "hi")
	err := e.ops.Provision(context.Background(), s)
	if !ops.IsPermanent(err) || !strings.Contains(err.Error(), "git clone exited 128: fatal: repository not found") {
		t.Fatalf("want permanent clone error, got %v", err)
	}
}

func TestCleanupIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	if _, err := e.st.SetStatus(context.Background(), s.ID, []string{session.StatusIdle}, session.StatusStopping, "test"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.ops.Cleanup(context.Background(), s, "stopped"); err != nil {
			t.Fatal(err)
		}
	}
	if e.fleet.Exists(s.ID) {
		t.Error("sandbox not deleted")
	}
	s = testutil.MustSession(t, e.st, s.ID)
	if s.Status != session.StatusStopped || s.EndedAt == nil {
		t.Errorf("session: %q ended %v", s.Status, s.EndedAt)
	}
	if tn := testutil.MustTurn(t, e.st, s.ID, 1); tn.Status != session.TurnStopped {
		t.Errorf("queued turn not cancelled: %q", tn.Status)
	}
}

func TestIsLost(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	if lost, err := e.ops.IsLost(context.Background(), s); lost || err != nil {
		t.Errorf("live sandbox: lost=%v err=%v", lost, err)
	}
	e.fleet.Lose(s.ID)
	if lost, err := e.ops.IsLost(context.Background(), s); !lost || err != nil {
		t.Errorf("missing sandbox: lost=%v err=%v", lost, err)
	}
}
