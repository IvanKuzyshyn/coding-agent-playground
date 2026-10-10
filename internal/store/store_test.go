package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"terrarium/internal/session"
	"terrarium/internal/store"
	"terrarium/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	st := testutil.NewStore(t)
	applied, err := st.Migrate(context.Background())
	if err != nil || len(applied) != 0 {
		t.Errorf("re-migrate applied %v, err %v", applied, err)
	}
}

func TestSetStatusIsConditional(t *testing.T) {
	t.Parallel()
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")

	if ok, err := st.SetStatus(ctx, s.ID, []string{session.StatusIdle}, session.StatusRunning, "x"); ok || err != nil {
		t.Errorf("transition from the wrong state applied: %v %v", ok, err)
	}
	if ok, err := st.SetStatus(ctx, s.ID, []string{session.StatusPending}, session.StatusStopping, "x"); !ok || err != nil {
		t.Fatalf("pending→stopping: %v %v", ok, err)
	}
	if ok, _ := st.Fail(ctx, s.ID, "boom"); ok {
		t.Error("Fail applied to a stopping session")
	}
	if got := testutil.MustSession(t, st, s.ID); got.Error != "" {
		t.Errorf("Fail wrote an error on a stopping session: %q", got.Error)
	}
	if ok, _ := st.SetStatus(ctx, s.ID, []string{session.StatusStopping}, session.StatusStopped, "x"); !ok {
		t.Fatal("stopping→stopped")
	}
	got := testutil.MustSession(t, st, s.ID)
	if got.EndedAt == nil {
		t.Error("ended_at not set")
	}
	// Only applied transitions are recorded.
	if want := []string{"pending", "stopping", "stopped"}; !slices.Equal(testutil.Statuses(testutil.MustEvents(t, st, s.ID)), want) {
		t.Errorf("statuses %v", testutil.Statuses(testutil.MustEvents(t, st, s.ID)))
	}
	if _, err := st.EnqueueTurn(ctx, s.ID, "late"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("enqueue on stopped session: %v", err)
	}
	if n, err := st.CountActiveByOwner(ctx, "alice"); n != 0 || err != nil {
		t.Errorf("active count %d %v", n, err)
	}
}

func TestTurnTerminalWriteOnce(t *testing.T) {
	t.Parallel()
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	if _, err := st.EnqueueTurn(ctx, s.ID, "second"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.RequestStopCurrentTurn(ctx, s.ID); ok {
		t.Error("stop requested with no running turn")
	}
	if ok, err := st.StartTurn(ctx, s.ID, 1, "turn-1"); !ok || err != nil {
		t.Fatalf("start: %v %v", ok, err)
	}
	if ok, _ := st.StartTurn(ctx, s.ID, 1, "turn-1"); ok {
		t.Error("turn started twice")
	}
	if tn, _ := st.ActiveTurn(ctx, s.ID); tn.Seq != 1 {
		t.Errorf("active turn %d, want running turn 1", tn.Seq)
	}
	if ok, _ := st.FinishTurn(ctx, s.ID, 1, session.TurnComplete, "ok", "", map[string]int{"input_tokens": 1}); !ok {
		t.Fatal("finish")
	}
	if ok, _ := st.FinishTurn(ctx, s.ID, 1, session.TurnFailed, "", "late", nil); ok {
		t.Error("second terminal write applied")
	}
	if tn := testutil.MustTurn(t, st, s.ID, 1); tn.Status != session.TurnComplete || tn.Result != "ok" || string(tn.Usage) != `{"input_tokens": 1}` {
		t.Errorf("turn 1: %+v usage %s", tn, tn.Usage)
	}
	if tn, _ := st.ActiveTurn(ctx, s.ID); tn.Seq != 2 {
		t.Errorf("active turn %d, want queued turn 2", tn.Seq)
	}
	if err := st.CancelQueuedTurns(ctx, s.ID, session.TurnStopped, "ended"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveTurn(ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("active turn after cancel: %v", err)
	}
	if tn := testutil.MustTurn(t, st, s.ID, 1); tn.Status != session.TurnComplete {
		t.Errorf("cancel touched a finished turn: %q", tn.Status)
	}
}

func TestHarnessSessionSetOnce(t *testing.T) {
	t.Parallel()
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	for _, hs := range []string{"first", "second"} {
		if err := st.SetHarnessSession(ctx, s.ID, hs); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.MustSession(t, st, s.ID).HarnessSession; got != "first" {
		t.Errorf("harness session %q", got)
	}
}
