package supervisor_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"

	"terrarium/internal/harness"
	"terrarium/internal/ops"
	"terrarium/internal/sandboxclient"
	"terrarium/internal/session"
	"terrarium/internal/store"
	"terrarium/internal/supervisor"
	"terrarium/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

const wait = 10 * time.Second

type env struct {
	st    *store.Store
	fleet *testutil.Fleet
	ops   *ops.Ops
}

func newEnv(t *testing.T) env {
	t.Helper()
	st := testutil.NewStore(t)
	fleet := testutil.NewFleet()
	return env{st: st, fleet: fleet, ops: &ops.Ops{
		Store:        st,
		Sandboxes:    fleet,
		NewClient:    func(endpoint, token string) ops.SandboxAPI { return fleet.Client(endpoint, token) },
		Gateway:      harness.Gateway{URL: "http://gateway:8080", ProxyHost: "gateway:3128"},
		SessionKey:   []byte("test-session-key"),
		Log:          testutil.Logger(),
		PollInterval: 2 * time.Millisecond,
		StopGrace:    30 * time.Millisecond,
	}}
}

// start runs a supervisor until the returned stop function (also registered as cleanup) is called.
func (e env) start(t *testing.T) (stop func()) {
	t.Helper()
	sup := &supervisor.Supervisor{Store: e.st, Ops: e.ops, Log: testutil.Logger(), Interval: 10 * time.Millisecond, IdlePoll: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("supervisor: %v", err)
			}
		case <-time.After(wait):
			t.Error("supervisor did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func (e env) waitStatus(t *testing.T, id uuid.UUID, status string) store.Session {
	t.Helper()
	var s store.Session
	testutil.Eventually(t, wait, "session "+status, func() bool {
		s = testutil.MustSession(t, e.st, id)
		return s.Status == status
	})
	return s
}

func (e env) waitTurn(t *testing.T, id uuid.UUID, seq int, status string) store.Turn {
	t.Helper()
	var tn store.Turn
	testutil.Eventually(t, wait, "turn "+status, func() bool {
		tn = testutil.MustTurn(t, e.st, id, seq)
		return tn.Status == status
	})
	return tn
}

// waitStarted waits until the turn's harness process exists in the sandbox.
func (e env) waitStarted(t *testing.T, id uuid.UUID, seq int) {
	t.Helper()
	testutil.Eventually(t, wait, "turn process", func() bool {
		_, ok := e.fleet.Command(id, "turn-"+string(rune('0'+seq)))
		return ok
	})
}

func turns(progs ...testutil.Program) func(uuid.UUID, sandboxclient.CommandRequest) testutil.Program {
	return func(_ uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		for i, p := range progs {
			if req.ID == "turn-"+string(rune('1'+i)) {
				return p
			}
		}
		return nil
	}
}

func claudeTurn() testutil.Program {
	return testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Pause(),
		testutil.Line(testutil.ClaudeMessage),
		testutil.Line(testutil.ClaudeResult),
		testutil.Exit(0),
	}
}

func blockingTurn() testutil.Program {
	return testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.WaitSignal(false)}
}

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn(), claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")

	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	e.waitStatus(t, s.ID, session.StatusIdle)
	_, err := e.st.EnqueueTurn(context.Background(), s.ID, "again")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(e.waitTurn(t, s.ID, 2, session.TurnComplete).Result).To(Equal("ok"), "turn 2 result")
	e.waitStatus(t, s.ID, session.StatusIdle)
	cmd, _ := e.fleet.Command(s.ID, "turn-2")
	i := slices.Index(cmd.Argv, "--resume")
	g.Expect(i).To(BeNumerically(">=", 0), "turn 2 argv: %v", cmd.Argv)
	g.Expect(cmd.Argv[i+1]).To(Equal(testutil.ClaudeSessionID), "turn 2 argv")
	g.Expect(testutil.Statuses(testutil.MustEvents(t, e.st, s.ID))).To(Equal([]string{"pending", "provisioning", "idle", "running", "idle", "running", "idle"}))
}

func TestRestartReattachesToRunningTurn(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	gate := make(chan struct{})
	e.fleet.Program = turns(testutil.Program{
		testutil.Line(testutil.ClaudeInit),
		testutil.Gate(gate),
		testutil.Line(testutil.ClaudeResult),
		testutil.Exit(0),
	})
	stop := e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	testutil.Eventually(t, wait, "init consumed", func() bool { return testutil.MustTurn(t, e.st, s.ID, 1).LogOffset > 0 })
	stop()

	e.start(t)
	close(gate)
	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	e.waitStatus(t, s.ID, session.StatusIdle)
	g.Expect(e.fleet.Starts(s.ID)).To(Equal(1), "harness processes started")
	var harnessEvents int
	for _, ev := range testutil.MustEvents(t, e.st, s.ID) {
		if ev.Kind == store.EventHarness {
			harnessEvents++
		}
	}
	g.Expect(harnessEvents).To(Equal(2), "harness events")
}

func TestDeleteDuringRunningTurn(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	var turn2Started atomic.Bool
	e.fleet.Program = func(id uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		if req.ID == "turn-2" {
			turn2Started.Store(true)
		}
		return turns(blockingTurn())(id, req)
	}
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	_, err := e.st.EnqueueTurn(context.Background(), s.ID, "queued follow-up")
	g.Expect(err).NotTo(HaveOccurred())
	e.waitStarted(t, s.ID, 1)
	e.waitStatus(t, s.ID, session.StatusRunning)
	// What DELETE /sessions/{id} does.
	ok, err := e.st.SetStatus(context.Background(), s.ID, []string{session.StatusRunning}, session.StatusStopping, "deleted by alice")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeTrue(), "set stopping")

	e.waitStatus(t, s.ID, session.StatusStopped)
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox not deleted")
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnStopped), "turn 1")
	g.Expect(tn.Error).To(Equal("session stopping"), "turn 1")
	tn = testutil.MustTurn(t, e.st, s.ID, 2)
	g.Expect(tn.Status).To(Equal(session.TurnStopped), "turn 2")
	g.Expect(tn.Error).To(HavePrefix("session ended"), "turn 2")
	g.Expect(turn2Started.Load()).To(BeFalse(), "queued turn started after the session was deleted")
}

func TestLostSandboxMidTurn(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(blockingTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	e.waitStarted(t, s.ID, 1)
	e.fleet.Lose(s.ID)

	s = e.waitStatus(t, s.ID, session.StatusFailed)
	g.Expect(s.Error).To(Equal("sandbox lost"), "session error")
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnFailed))
	g.Expect(tn.Error).To(Equal("sandbox lost"), "turn error")
	g.Expect(testutil.Statuses(testutil.MustEvents(t, e.st, s.ID))).To(Equal([]string{"pending", "provisioning", "idle", "running", "stopping", "failed"}))
}

func TestProvisionRetriesTransientCreateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.fleet.CreateErr = func(call int) error {
		if call == 1 {
			return errors.New("docker: daemon busy")
		}
		return nil
	}
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")

	testutil.Eventually(t, wait, "attempt recorded", func() bool { return testutil.MustSession(t, e.st, s.ID).Attempts == 1 })
	s = testutil.MustSession(t, e.st, s.ID)
	g.Expect(s.NextAttemptAt).NotTo(BeNil(), "backoff not recorded")
	g.Expect(time.Until(*s.NextAttemptAt)).To(BeNumerically(">=", time.Second), "backoff not recorded")
	g.Expect(s.Error).To(Equal("docker: daemon busy"))
	g.Expect(e.fleet.Creates()).To(Equal(1), "retried before the backoff elapsed")

	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	s = testutil.MustSession(t, e.st, s.ID)
	g.Expect(e.fleet.Creates()).To(Equal(2), "creates")
	g.Expect(e.fleet.Count()).To(Equal(1), "sandboxes")
	g.Expect(s.Attempts).To(BeZero(), "attempts not reset")
	g.Expect(s.NextAttemptAt).To(BeNil(), "attempts not reset")
	g.Expect(s.Error).To(BeEmpty(), "attempts not reset")
}

func TestProvisionFailsAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.CreateErr = func(int) error { return errors.New("docker: no space left") }
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	// Two attempts already failed; the next is the last.
	_, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET attempts = 2 WHERE id = $1`, s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	e.start(t)
	s = e.waitStatus(t, s.ID, session.StatusFailed)
	g.Expect(s.Error).To(ContainSubstring("provision failed after 3 attempts"))
	g.Expect(testutil.MustTurn(t, e.st, s.ID, 1).Status).To(Equal(session.TurnStopped), "turn 1")
}

func TestProvisionCloneFailureFailsSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = func(_ uuid.UUID, req sandboxclient.CommandRequest) testutil.Program {
		if req.ID == "provision-clone" {
			return testutil.Program{testutil.Stderr("fatal: repository 'https://github.com/octo/missing.git/' not found\n"), testutil.Exit(128)}
		}
		return claudeTurn()
	}
	spec := testutil.Spec(session.HarnessClaude)
	spec.Repo = "octo/missing"
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", spec, "hi")

	s = e.waitStatus(t, s.ID, session.StatusFailed)
	g.Expect(s.Error).To(HavePrefix("git clone exited 128: fatal: repository"))
	g.Expect(e.fleet.Creates()).To(Equal(1), "creates")
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox exists")
	g.Expect(testutil.Statuses(testutil.MustEvents(t, e.st, s.ID))).To(Equal([]string{"pending", "provisioning", "stopping", "failed"}))
}

func TestOrphanSandboxesRemoved(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())

	unknown := uuid.New()
	e.fleet.AddOrphan(unknown)
	ended := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	for _, to := range []string{session.StatusStopping, session.StatusStopped} {
		_, err := e.st.SetStatus(context.Background(), ended.ID, []string{session.StatusPending, session.StatusStopping}, to, "test")
		g.Expect(err).NotTo(HaveOccurred())
	}
	e.fleet.AddOrphan(ended.ID)

	e.start(t)
	live := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	e.waitTurn(t, live.ID, 1, session.TurnComplete)
	testutil.Eventually(t, wait, "orphans removed", func() bool { return !e.fleet.Exists(unknown) && !e.fleet.Exists(ended.ID) })
	g.Expect(e.fleet.Exists(live.ID)).To(BeTrue(), "live session's sandbox removed")
}

func TestIdleTimeout(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	e.waitStatus(t, s.ID, session.StatusIdle)
	past := time.Now().Add(-time.Second)
	g.Expect(e.st.SetIdleUntil(context.Background(), s.ID, &past)).To(Succeed())
	e.waitStatus(t, s.ID, session.StatusComplete)
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox not deleted")
	g.Expect(hasLifecycle(t, e.st, s.ID, session.StatusStopping, "idle timeout")).To(BeTrue(), "no stopping event for idle timeout")
}

func TestTTLExpiryIdleSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	e.waitStatus(t, s.ID, session.StatusIdle)
	_, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	e.waitStatus(t, s.ID, session.StatusComplete)
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox not deleted")
	g.Expect(hasLifecycle(t, e.st, s.ID, session.StatusStopping, "session ttl expired")).To(BeTrue(), "no stopping event for ttl")
}

func hasLifecycle(t *testing.T, st *store.Store, id uuid.UUID, status, message string) bool {
	for _, ev := range testutil.MustEvents(t, st, id) {
		p := testutil.Payload(ev)
		if ev.Kind == store.EventLifecycle && p["status"] == status && p["message"] == message {
			return true
		}
	}
	return false
}

func TestSecondSupervisorWaitsForLock(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	stop1 := e.start(t)
	waiting := func() bool {
		var n int
		err := e.st.Pool().QueryRow(context.Background(), `
			SELECT count(*) FROM pg_locks l JOIN pg_database d ON d.oid = l.database
			WHERE l.locktype = 'advisory' AND l.objid = 727002 AND NOT l.granted AND d.datname = current_database()`).Scan(&n)
		g.Expect(err).NotTo(HaveOccurred())
		return n == 1
	}
	testutil.Eventually(t, wait, "first supervisor running", func() bool {
		e.fleet.AddOrphan(uuid.New())
		time.Sleep(20 * time.Millisecond)
		return e.fleet.Count() == 0
	})

	e.start(t)
	testutil.Eventually(t, wait, "second supervisor blocked on the lock", waiting)
	stop1()
	testutil.Eventually(t, wait, "second supervisor took over", func() bool { return !waiting() })
	orphan := uuid.New()
	e.fleet.AddOrphan(orphan)
	testutil.Eventually(t, wait, "second supervisor reconciles", func() bool { return !e.fleet.Exists(orphan) })
}

// Plan §4.2 step 2: sessions in idle|running with no container → failed, then stopped. Only the
// running case is detected (when a turn's sandboxd call fails); an idle session whose sandbox
// vanished stays idle until its idle timeout.
func TestLostSandboxWhileIdle(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	e.waitTurn(t, s.ID, 1, session.TurnComplete)
	e.waitStatus(t, s.ID, session.StatusIdle)
	e.fleet.Lose(s.ID)
	g.Expect(e.waitStatus(t, s.ID, session.StatusFailed).Error).To(Equal("sandbox lost"))
}
