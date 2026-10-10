package supervisor_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"terrarium/internal/session"
	"terrarium/internal/testutil"
)

// defaultLifecycle mirrors config/presets.yaml.
var defaultLifecycle = session.Lifecycle{
	OnComplete: session.ActionTerminate,
	OnFailure:  session.ActionTerminate,
	OnStop:     session.ActionKeepIdle,
}

func specWith(l session.Lifecycle) session.Spec {
	s := testutil.Spec(session.HarnessClaude)
	s.Lifecycle = l
	return s
}

func maxTurnsTurn() testutil.Program {
	return testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Line(testutil.ClaudeMaxTurns), testutil.Exit(1)}
}

func TestCompletedTurnCompletesSessionAndDestroysSandbox(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")

	s = e.waitStatus(t, s.ID, session.StatusComplete)
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox exists")
	g.Expect(s.EndedAt).NotTo(BeNil())
	g.Expect(s.Error).To(BeEmpty())
	_, err := e.st.EnqueueTurn(context.Background(), s.ID, "more")
	g.Expect(err).To(HaveOccurred(), "a complete session accepted a follow-up")
}

func TestKeepIdleKeepsSandboxAndEndsWithLastTurnOutcome(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn(), maxTurnsTurn())
	e.start(t)
	a := testutil.CreateSession(t, e.st, "alice", specWith(session.KeepIdleLifecycle), "hi")
	e.waitTurn(t, a.ID, 1, session.TurnComplete)
	e.waitStatus(t, a.ID, session.StatusIdle)
	g.Expect(e.fleet.Exists(a.ID)).To(BeTrue(), "keep_idle sandbox destroyed")
	// Deleting a kept-idle session ends it with its last turn's outcome.
	_, err := e.st.EndSession(context.Background(), a.ID, "", "deleted", "")
	g.Expect(err).NotTo(HaveOccurred())
	e.waitStatus(t, a.ID, session.StatusComplete)

	b := testutil.CreateSession(t, e.st, "alice", specWith(session.KeepIdleLifecycle), "hi")
	e.waitTurn(t, b.ID, 1, session.TurnComplete)
	_, err = e.st.EnqueueTurn(context.Background(), b.ID, "too much")
	g.Expect(err).NotTo(HaveOccurred())
	e.waitTurn(t, b.ID, 2, session.TurnLimitExceeded)
	e.waitStatus(t, b.ID, session.StatusIdle)
	_, err = e.st.EndSession(context.Background(), b.ID, "", "deleted", "")
	g.Expect(err).NotTo(HaveOccurred())
	e.waitStatus(t, b.ID, session.StatusFailed)
}

func TestFailedTurnFailsSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(maxTurnsTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")

	s = e.waitStatus(t, s.ID, session.StatusFailed)
	g.Expect(s.Error).To(HavePrefix("turn 1 limit_exceeded: max agent steps reached"))
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox exists")
}

func TestStoppedTurnKeepsSandboxIdle(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	e.fleet.Program = turns(blockingTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")
	e.waitStarted(t, s.ID, 1)
	_, err := e.st.RequestStopCurrentTurn(context.Background(), s.ID)
	g.Expect(err).NotTo(HaveOccurred())
	e.waitTurn(t, s.ID, 1, session.TurnStopped)
	e.waitStatus(t, s.ID, session.StatusIdle)
	g.Expect(e.fleet.Exists(s.ID)).To(BeTrue(), "on_stop keep_idle destroyed the sandbox")
}

func TestQueuedFollowUpRunsBeforeCompletion(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	gate := make(chan struct{})
	first := testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Gate(gate), testutil.Line(testutil.ClaudeResult), testutil.Exit(0)}
	e.fleet.Program = turns(first, claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")
	e.waitStarted(t, s.ID, 1)
	_, err := e.st.EnqueueTurn(context.Background(), s.ID, "and then")
	g.Expect(err).NotTo(HaveOccurred())
	close(gate)

	e.waitStatus(t, s.ID, session.StatusComplete)
	g.Expect(testutil.MustTurn(t, e.st, s.ID, 2).Status).To(Equal(session.TurnComplete), "follow-up turn")
}

func TestAgentReportedFailureFailsTurnAndSession(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t)
	result := `{"type":"result","subtype":"success","is_error":false,"result":"No access.\nTERRARIUM_OUTCOME: failed - wttr.in is blocked","num_turns":1,"session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab"}`
	e.fleet.Program = turns(testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Line(result), testutil.Exit(0)})
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "weather?")

	s = e.waitStatus(t, s.ID, session.StatusFailed)
	tn := testutil.MustTurn(t, e.st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnFailed))
	g.Expect(tn.Error).To(Equal("agent reported failure: wttr.in is blocked"))
	g.Expect(tn.Result).To(Equal("No access."))
	g.Expect(s.Error).To(ContainSubstring("agent reported failure"))
	g.Expect(e.fleet.Exists(s.ID)).To(BeFalse(), "sandbox exists")
}
