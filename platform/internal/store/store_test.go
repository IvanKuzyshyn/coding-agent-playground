package store_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"terrarium/internal/session"
	"terrarium/internal/store"
	"terrarium/internal/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	st := testutil.NewStore(t)
	applied, err := st.Migrate(context.Background())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(applied).To(BeEmpty(), "re-migrate applied migrations")
}

func TestSetStatusIsConditional(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")

	ok, err := st.SetStatus(ctx, s.ID, []string{session.StatusIdle}, session.StatusRunning, "x")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeFalse(), "transition from the wrong state applied")

	ok, err = st.SetStatus(ctx, s.ID, []string{session.StatusPending}, session.StatusStopping, "x")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeTrue(), "pending→stopping")

	ok, _ = st.Fail(ctx, s.ID, "boom")
	g.Expect(ok).To(BeFalse(), "Fail applied to a stopping session")
	g.Expect(testutil.MustSession(t, st, s.ID).Error).To(BeEmpty(), "Fail wrote an error on a stopping session")

	ok, _ = st.SetStatus(ctx, s.ID, []string{session.StatusStopping}, session.StatusStopped, "x")
	g.Expect(ok).To(BeTrue(), "stopping→stopped")
	g.Expect(testutil.MustSession(t, st, s.ID).EndedAt).NotTo(BeNil(), "ended_at not set")
	// Only applied transitions are recorded.
	g.Expect(testutil.Statuses(testutil.MustEvents(t, st, s.ID))).To(Equal([]string{"pending", "stopping", "stopped"}))

	_, err = st.EnqueueTurn(ctx, s.ID, "late")
	g.Expect(err).To(MatchError(store.ErrConflict), "enqueue on stopped session")

	n, err := st.CountActiveByOwner(ctx, "alice")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(n).To(BeZero(), "active count")
}

func TestTurnTerminalWriteOnce(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	_, err := st.EnqueueTurn(ctx, s.ID, "second")
	g.Expect(err).NotTo(HaveOccurred())

	ok, _ := st.RequestStopCurrentTurn(ctx, s.ID)
	g.Expect(ok).To(BeFalse(), "stop requested with no running turn")

	ok, err = st.StartTurn(ctx, s.ID, 1, "turn-1")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ok).To(BeTrue(), "start")

	ok, _ = st.StartTurn(ctx, s.ID, 1, "turn-1")
	g.Expect(ok).To(BeFalse(), "turn started twice")

	tn, _ := st.ActiveTurn(ctx, s.ID)
	g.Expect(tn.Seq).To(Equal(1), "active turn, want running turn 1")

	ok, _ = st.FinishTurn(ctx, s.ID, 1, session.TurnComplete, "ok", "", map[string]int{"input_tokens": 1})
	g.Expect(ok).To(BeTrue(), "finish")

	ok, _ = st.FinishTurn(ctx, s.ID, 1, session.TurnFailed, "", "late", nil)
	g.Expect(ok).To(BeFalse(), "second terminal write applied")

	tn = testutil.MustTurn(t, st, s.ID, 1)
	g.Expect(tn.Status).To(Equal(session.TurnComplete))
	g.Expect(tn.Result).To(Equal("ok"))
	g.Expect(string(tn.Usage)).To(Equal(`{"input_tokens": 1}`))

	tn, _ = st.ActiveTurn(ctx, s.ID)
	g.Expect(tn.Seq).To(Equal(2), "active turn, want queued turn 2")

	g.Expect(st.CancelQueuedTurns(ctx, s.ID, session.TurnStopped, "ended")).To(Succeed())
	_, err = st.ActiveTurn(ctx, s.ID)
	g.Expect(err).To(MatchError(store.ErrNotFound), "active turn after cancel")
	g.Expect(testutil.MustTurn(t, st, s.ID, 1).Status).To(Equal(session.TurnComplete), "cancel touched a finished turn")
}

func TestHarnessSessionSetOnce(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	st := testutil.NewStore(t)
	ctx := context.Background()
	s := testutil.CreateSession(t, st, "alice", testutil.Spec(session.HarnessClaude), "hi")
	for _, hs := range []string{"first", "second"} {
		g.Expect(st.SetHarnessSession(ctx, s.ID, hs)).To(Succeed())
	}
	g.Expect(testutil.MustSession(t, st, s.ID).HarnessSession).To(Equal("first"))
}
