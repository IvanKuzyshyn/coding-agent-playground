package ops_test

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"

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
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turnPrograms(claudeHappy())
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))

	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred(), "run turn")

	s = testutil.MustSession(t, e.st, s.ID)
	g.Expect(s.Status).To(Equal(session.StatusIdle))
	g.Expect(s.HarnessSession).To(Equal(testutil.ClaudeSessionID))
	g.Expect(s.IdleUntil).NotTo(BeNil(), "idle_until not set from the idle timeout")
	g.Expect(time.Until(*s.IdleUntil)).To(BeNumerically(">=", 59*time.Minute), "idle_until not set from the idle timeout")
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnComplete))
	g.Expect(tn.Result).To(Equal("ok"))
	g.Expect(tn.Error).To(BeEmpty())
	g.Expect(tn.CommandID).To(Equal("turn-1"))
	var usage harness.Usage
	g.Expect(json.Unmarshal(tn.Usage, &usage)).To(Succeed(), "usage %s", tn.Usage)
	g.Expect(usage).To(Equal(harness.Usage{InputTokens: 14, OutputTokens: 272, CachedInputTokens: 100, AgentSteps: 3}))
	cmd, _ := e.fleet.Command(s.ID, "turn-1")
	g.Expect(cmd.Argv).NotTo(ContainElement("--resume"), "first turn argv")
	g.Expect(cmd.Argv[0]).To(Equal("claude"), "first turn argv")
	g.Expect(cmd.Argv[len(cmd.Argv)-1]).To(Equal("say ok"), "first turn argv")

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
	g.Expect(got).To(Equal(want), "events")
}

func TestHappyPathCodexAndResume(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
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
	_, ok := e.fleet.File(s.ID, harness.Home+"/.codex/config.toml")
	g.Expect(ok).To(BeTrue(), "codex config not written")

	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred(), "turn 1")
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnComplete), "turn 1")
	g.Expect(tn.Result).To(Equal("41"), "turn 1")
	var usage harness.Usage
	_ = json.Unmarshal(tn.Usage, &usage)
	g.Expect(usage.InputTokens).To(BeEquivalentTo(12926))
	g.Expect(usage.CachedInputTokens).To(BeEquivalentTo(9600))
	g.Expect(usage.OutputTokens).To(BeEquivalentTo(10))
	g.Expect(testutil.MustSession(t, e.st, s.ID).HarnessSession).To(Equal(testutil.CodexThreadID))

	_, err = e.st.EnqueueTurn(context.Background(), s.ID, "and again")
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred(), "turn 2")
	cmd, _ := e.fleet.Command(s.ID, "turn-2")
	g.Expect(cmd.Argv[:4]).To(Equal([]string{"codex", "exec", "resume", testutil.CodexThreadID}), "turn 2 argv")
	g.Expect(cmd.Argv[len(cmd.Argv)-1]).To(Equal("and again"), "turn 2 argv")
	g.Expect(testutil.MustTurn(t, e.st, s.ID, 2).Status).To(Equal(session.TurnComplete), "turn 2")
}

func TestFollowUpResumesClaudeSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turnPrograms(claudeHappy(), claudeHappy())
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.st.EnqueueTurn(context.Background(), s.ID, "follow up")
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	cmd, _ := e.fleet.Command(s.ID, "turn-2")
	i := slices.Index(cmd.Argv, "--resume")
	g.Expect(i).To(BeNumerically(">=", 0), "turn 2 argv: %v", cmd.Argv)
	g.Expect(cmd.Argv[i+1]).To(Equal(testutil.ClaudeSessionID), "turn 2 argv")
	g.Expect(cmd.Argv[len(cmd.Argv)-1]).To(Equal("follow up"), "turn 2 argv")
}

func TestReattachAfterCrash(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
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
	_, err := e.runActive(t, ctx, s.ID)
	g.Expect(err).To(MatchError(context.Canceled), "first run")
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnRunning), "after crash")
	g.Expect(tn.LogOffset).To(Equal(consumed), "after crash")
	g.Expect(tn.ErrOffset).To(Equal(int64(len("first stderr\n"))), "after crash")

	_, err = e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred(), "reattach")
	g.Expect(e.fleet.Starts(s.ID)).To(Equal(1), "processes started")
	tn = testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnComplete))
	g.Expect(tn.Result).To(Equal("ok"))
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
	g.Expect(turnEvents).To(Equal(want), "turn events")
	g.Expect(testutil.Statuses(testutil.MustEvents(t, e.st, s.ID))).To(Equal([]string{"pending", "provisioning", "idle", "running", "idle"}), "session statuses")
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
			g := NewWithT(t)
			e := newEnv(t)
			e.fleet.Program = turnPrograms(blockingTurn(tc.ignoreTerm))
			s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
			stopOnFirstRead(e, func() {
				if ok, err := e.st.RequestStopCurrentTurn(context.Background(), s.ID); err != nil || !ok {
					t.Errorf("request stop: %v %v", ok, err)
				}
			})
			start := time.Now()
			_, err := e.runActive(t, context.Background(), s.ID)
			g.Expect(err).NotTo(HaveOccurred())
			cmd, _ := e.fleet.Command(s.ID, "turn-1")
			g.Expect(cmd.Signals).To(Equal(tc.signals), "signals")
			if tc.ignoreTerm {
				g.Expect(time.Since(start)).To(BeNumerically(">=", e.ops.StopGrace), "KILL before the grace period")
			}
			tn := testutil.MustTurn(t, e.st, s.ID, 1)
			g.Expect(tn.Status).To(Equal(session.TurnStopped))
			g.Expect(tn.Error).To(Equal("stopped by engineer"))
			g.Expect(tn.Result).To(Equal("ok"))
			g.Expect(testutil.MustSession(t, e.st, s.ID).Status).To(Equal(session.StatusIdle), "session status")
		})
	}
}

func TestTokenBudgetExhausted(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
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
	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnLimitExceeded))
	g.Expect(tn.Error).To(Equal("session token budget exhausted"))
	cmd, _ := e.fleet.Command(s.ID, "turn-1")
	g.Expect(cmd.Signals).To(Equal([]string{"TERM"}), "signals")
	g.Expect(testutil.MustSession(t, e.st, s.ID).Status).To(Equal(session.StatusIdle), "session status")
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
			g := NewWithT(t)
			e := newEnv(t)
			e.fleet.Program = turnPrograms(tc.prog)
			s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
			_, err := e.runActive(t, context.Background(), s.ID)
			g.Expect(err).NotTo(HaveOccurred())
			tn := testutil.MustTurn(t, e.st, s.ID, 1)
			g.Expect(tn.Status).To(Equal(tc.status))
			g.Expect(tn.Error).To(Equal(tc.errMsg))
			g.Expect(testutil.MustSession(t, e.st, s.ID).Status).To(Equal(session.StatusIdle), "session status")
		})
	}
}

// A harness that writes more than one log chunk (1 MiB) and exits before the tail catches up:
// the tail must keep reading until sandboxd returns no data, not stop after the first chunk.
func TestOutputLargerThanOneChunkAfterExit(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
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
	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnComplete), "turn error %q", tn.Error)
	g.Expect(tn.Result).To(Equal("ok"))
}

func TestTTLExpiryStopsRunningTurn(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turnPrograms(blockingTurn(false))
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	stopOnFirstRead(e, func() {
		if _, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID); err != nil {
			t.Error(err)
		}
	})
	_, err := e.runActive(t, context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnStopped))
	g.Expect(tn.Error).To(Equal("session ttl expired"))
	g.Expect(testutil.MustSession(t, e.st, s.ID).Status).To(Equal(session.StatusStopping), "session status")
}

func TestProvisionIdempotent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	spec := testutil.Spec(session.HarnessCodex)
	spec.Repo = "octo/hello"
	s := testutil.CreateSession(t, e.st, "alice", spec, "hi")
	for i := range 2 {
		g.Expect(e.ops.Provision(context.Background(), s)).To(Succeed(), "provision %d", i+1)
	}
	g.Expect(e.fleet.Count()).To(Equal(1), "sandboxes")
	g.Expect(e.fleet.Starts(s.ID)).To(Equal(1), "clone started more than once")
	cmd, _ := e.fleet.Command(s.ID, "provision-clone")
	g.Expect(cmd.Argv).To(ContainElement("https://github.com/octo/hello.git"), "clone argv")
	s = testutil.MustSession(t, e.st, s.ID)
	g.Expect(s.Status).To(Equal(session.StatusIdle))
	g.Expect(s.ContainerID).To(Equal(e.fleet.Name(s.ID)))
}

func TestProvisionCloneFailureIsPermanent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = func(_ uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		return testutil.Program{testutil.Stderr("Cloning into 'work'...\nfatal: repository not found\n"), testutil.Exit(128)}
	}
	spec := testutil.Spec(session.HarnessClaude)
	spec.Repo = "octo/missing"
	s := testutil.CreateSession(t, e.st, "alice", spec, "hi")
	err := e.ops.Provision(context.Background(), s)
	g.Expect(err).To(MatchError(ContainSubstring("git clone exited 128: fatal: repository not found")))
	g.Expect(ops.IsPermanent(err)).To(BeTrue(), "want permanent clone error, got %v", err)
}

func TestCleanupIdempotent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	_, err := e.st.SetStatus(context.Background(), s.ID, []string{session.StatusIdle}, session.StatusStopping, "test")
	g.Expect(err).NotTo(HaveOccurred())
	for range 2 {
		g.Expect(e.ops.Cleanup(context.Background(), s)).To(Succeed())
	}
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox not deleted")
	s = testutil.MustSession(t, e.st, s.ID)
	g.Expect(s.Status).To(Equal(session.StatusStopped))
	g.Expect(s.EndedAt).NotTo(BeNil())
	g.Expect(testutil.MustTurn(t, e.st, s.ID, 1).Status).To(Equal(session.TurnStopped), "queued turn not cancelled")
}

func TestIsLost(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	lost, err := e.ops.IsLost(context.Background(), s)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lost).To(BeFalse(), "live sandbox")
	e.fleet.Lose(s.ID)
	lost, err = e.ops.IsLost(context.Background(), s)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(lost).To(BeTrue(), "missing sandbox")
}

// A worker that read the session as idle just before DELETE moved it to stopping must not start
// the harness, but RunTurn ignores that its idle→running transition did not apply.
func TestRunTurnDoesNotStartOnStoppingSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turnPrograms(claudeHappy())
	s := e.provisioned(t, testutil.Spec(session.HarnessClaude))
	tn, err := e.st.ActiveTurn(context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.st.SetStatus(context.Background(), s.ID, []string{session.StatusIdle}, session.StatusStopping, "deleted by alice")
	g.Expect(err).NotTo(HaveOccurred())
	_ = e.ops.RunTurn(context.Background(), s, tn) // s is the stale idle snapshot
	g.Expect(e.fleet.Starts(s.ID)).To(BeZero(), "harness started on a stopping session")
}
