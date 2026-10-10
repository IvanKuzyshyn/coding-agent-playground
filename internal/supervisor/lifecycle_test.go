package supervisor_test

import (
	"context"
	"strings"
	"testing"

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
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")

	s = e.waitStatus(t, s.ID, session.StatusComplete)
	if e.fleet.Exists(s.ID) || s.EndedAt == nil || s.Error != "" {
		t.Errorf("sandbox exists=%v ended=%v error=%q", e.fleet.Exists(s.ID), s.EndedAt, s.Error)
	}
	if _, err := e.st.EnqueueTurn(context.Background(), s.ID, "more"); err == nil {
		t.Error("a complete session accepted a follow-up")
	}
}

func TestKeepIdleKeepsSandboxAndEndsWithLastTurnOutcome(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turns(claudeTurn(), maxTurnsTurn())
	e.start(t)
	a := testutil.CreateSession(t, e.st, "alice", specWith(session.KeepIdleLifecycle), "hi")
	e.waitTurn(t, a.ID, 1, session.TurnComplete)
	e.waitStatus(t, a.ID, session.StatusIdle)
	if !e.fleet.Exists(a.ID) {
		t.Fatal("keep_idle sandbox destroyed")
	}
	// Deleting a kept-idle session ends it with its last turn's outcome.
	if _, err := e.st.EndSession(context.Background(), a.ID, "", "deleted", ""); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(t, a.ID, session.StatusComplete)

	b := testutil.CreateSession(t, e.st, "alice", specWith(session.KeepIdleLifecycle), "hi")
	e.waitTurn(t, b.ID, 1, session.TurnComplete)
	if _, err := e.st.EnqueueTurn(context.Background(), b.ID, "too much"); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(t, b.ID, 2, session.TurnLimitExceeded)
	e.waitStatus(t, b.ID, session.StatusIdle)
	if _, err := e.st.EndSession(context.Background(), b.ID, "", "deleted", ""); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(t, b.ID, session.StatusFailed)
}

func TestFailedTurnFailsSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turns(maxTurnsTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")

	s = e.waitStatus(t, s.ID, session.StatusFailed)
	if !strings.HasPrefix(s.Error, "turn 1 limit_exceeded: max agent steps reached") || e.fleet.Exists(s.ID) {
		t.Errorf("error %q, sandbox exists %v", s.Error, e.fleet.Exists(s.ID))
	}
}

func TestStoppedTurnKeepsSandboxIdle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.fleet.Program = turns(blockingTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")
	e.waitStarted(t, s.ID, 1)
	if _, err := e.st.RequestStopCurrentTurn(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	e.waitTurn(t, s.ID, 1, session.TurnStopped)
	e.waitStatus(t, s.ID, session.StatusIdle)
	if !e.fleet.Exists(s.ID) {
		t.Error("on_stop keep_idle destroyed the sandbox")
	}
}

func TestQueuedFollowUpRunsBeforeCompletion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gate := make(chan struct{})
	first := testutil.Program{testutil.Line(testutil.ClaudeInit), testutil.Gate(gate), testutil.Line(testutil.ClaudeResult), testutil.Exit(0)}
	e.fleet.Program = turns(first, claudeTurn())
	e.start(t)
	s := testutil.CreateSession(t, e.st, "alice", specWith(defaultLifecycle), "hi")
	e.waitStarted(t, s.ID, 1)
	if _, err := e.st.EnqueueTurn(context.Background(), s.ID, "and then"); err != nil {
		t.Fatal(err)
	}
	close(gate)

	e.waitStatus(t, s.ID, session.StatusComplete)
	if tn := testutil.MustTurn(t, e.st, s.ID, 2); tn.Status != session.TurnComplete {
		t.Errorf("follow-up turn %s, want complete", tn.Status)
	}
}
