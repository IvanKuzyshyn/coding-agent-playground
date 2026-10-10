// Package supervisor drives session operations with a reconcile loop: Postgres and container
// labels are the source of truth, and every pass converges actual state towards it. This is the
// only package that knows a loop (not Temporal) drives the ops (plan §4.4).
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/ops"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// lockID makes sure only one supervisor runs against a database.
const lockID = 727_002

const maxProvisionAttempts = 3

type Supervisor struct {
	Store *store.Store
	Ops   *ops.Ops
	Log   *slog.Logger
	// Interval between reconcile passes.
	Interval time.Duration
	// IdlePoll is how often an idle session's worker checks for new turns.
	IdlePoll time.Duration

	mu      sync.Mutex
	workers map[uuid.UUID]context.CancelFunc
	wg      sync.WaitGroup
}

// Run blocks until ctx is cancelled. It takes a session-level advisory lock first, so a second
// supervisor waits instead of racing the first.
func (s *Supervisor) Run(ctx context.Context) error {
	conn, err := s.Store.Pool().Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	s.Log.Info("supervisor waiting for lock")
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID) //nolint:errcheck
	s.Log.Info("supervisor started")

	s.workers = map[uuid.UUID]context.CancelFunc{}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		// The advisory lock lives on this connection; if it dropped, another supervisor may now
		// hold the lock, so this one must stop rather than race it.
		if _, err := conn.Exec(ctx, `SELECT 1`); err != nil {
			s.stopWorkers()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("supervisor lock connection lost: %w", err)
		}
		if err := s.reconcile(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			s.stopWorkers()
			return nil
		case <-t.C:
		}
	}
}

func (s *Supervisor) reconcile(ctx context.Context) error {
	if err := s.removeOrphans(ctx); err != nil {
		s.Log.Error("remove orphans", "err", err)
	}
	sessions, err := s.Store.ActiveSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, ss := range sessions {
		if err := s.enforceDeadlines(ctx, ss, now); err != nil {
			s.Log.Error("deadlines", "session", ss.ID, "err", err)
		}
		if ss.Status == session.StatusIdle {
			// Running sessions notice a lost sandbox in their tail loop; idle ones only here.
			if lost, err := s.Ops.IsLost(ctx, ss); err != nil {
				s.Log.Error("check sandbox", "session", ss.ID, "err", err)
			} else if lost {
				if _, err := s.Store.Fail(ctx, ss.ID, "sandbox lost"); err != nil {
					s.Log.Error("fail lost session", "session", ss.ID, "err", err)
				}
			}
		}
		s.ensureWorker(ctx, ss.ID)
	}
	return nil
}

// removeOrphans deletes sandboxes whose session ended or never existed in this database.
func (s *Supervisor) removeOrphans(ctx context.Context) error {
	boxes, err := s.Ops.Sandboxes.List(ctx)
	if err != nil {
		return err
	}
	if len(boxes) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(boxes))
	for i, b := range boxes {
		ids[i] = b.SessionID
	}
	statuses, err := s.Store.SessionStatuses(ctx, ids)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		st, ok := statuses[b.SessionID]
		if ok && !session.Terminal(st) {
			continue
		}
		s.Log.Info("removing orphan sandbox", "session", b.SessionID, "known", ok)
		if err := s.Ops.Sandboxes.Delete(ctx, b.SessionID); err != nil {
			s.Log.Error("remove orphan", "session", b.SessionID, "err", err)
		}
	}
	return nil
}

func (s *Supervisor) enforceDeadlines(ctx context.Context, ss store.Session, now time.Time) error {
	switch {
	case ss.Status == session.StatusIdle && ss.IdleUntil != nil && now.After(*ss.IdleUntil):
		_, err := s.Store.EndSession(ctx, ss.ID, "", "idle timeout", "")
		return err
	case now.After(ss.ExpiresAt) && ss.Status != session.StatusStopping && !session.Terminal(ss.Status):
		// Also for running sessions: the tail loop stops the turn when it sees stopping, and this
		// still applies if the tail is stuck on an unresponsive sandboxd. The final status comes
		// from the last finished turn.
		_, err := s.Store.EndSession(ctx, ss.ID, "", "session ttl expired", "")
		return err
	}
	return nil
}

func (s *Supervisor) ensureWorker(ctx context.Context, id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workers[id]; ok {
		return
	}
	wctx, cancel := context.WithCancel(ctx)
	s.workers[id] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.workers, id)
			s.mu.Unlock()
			cancel()
		}()
		if err := s.work(wctx, id); err != nil && wctx.Err() == nil {
			s.Log.Error("session worker", "session", id, "err", err)
		}
	}()
}

func (s *Supervisor) stopWorkers() {
	s.mu.Lock()
	for _, cancel := range s.workers {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// work drives one session until it is stopped. Errors return to the reconcile loop, which
// starts a fresh worker on its next pass; all operations re-read state, so that is safe.
func (s *Supervisor) work(ctx context.Context, id uuid.UUID) error {
	for {
		ss, err := s.Store.GetSession(ctx, id)
		if err != nil {
			return err
		}
		switch ss.Status {
		case session.StatusComplete, session.StatusFailed, session.StatusStopped:
			return nil

		case session.StatusPending, session.StatusProvisioning:
			if ss.NextAttemptAt != nil && time.Now().Before(*ss.NextAttemptAt) {
				sleep(ctx, time.Until(*ss.NextAttemptAt))
				continue
			}
			if err := s.Ops.Provision(ctx, ss); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := s.provisionFailed(ctx, ss, err); err != nil {
					return err
				}
				continue
			}
			if err := s.Store.ResetAttempts(ctx, id); err != nil {
				return err
			}

		case session.StatusIdle, session.StatusRunning:
			t, err := s.Store.ActiveTurn(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				if ss.Status == session.StatusRunning {
					// A crash between finishing the turn and marking the session idle.
					if err := s.Ops.BecomeIdle(ctx, ss, []string{session.StatusRunning}, "no active turn"); err != nil {
						return err
					}
				}
				sleep(ctx, s.IdlePoll)
				continue
			}
			if err != nil {
				return err
			}
			if err := s.Ops.RunTurn(ctx, ss, t); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return s.turnFailed(ctx, ss, t, err)
			}

		case session.StatusStopping:
			return s.Ops.Cleanup(ctx, ss)
		}
	}
}

func (s *Supervisor) provisionFailed(ctx context.Context, ss store.Session, cause error) error {
	s.Log.Warn("provision failed", "session", ss.ID, "err", cause)
	if ops.IsPermanent(cause) {
		_, err := s.Store.Fail(ctx, ss.ID, cause.Error())
		return err
	}
	n, err := s.Store.RecordAttemptFailure(ctx, ss.ID, cause.Error(), backoff(ss.Attempts))
	if err != nil {
		return err
	}
	if n >= maxProvisionAttempts {
		_, err := s.Store.Fail(ctx, ss.ID, fmt.Sprintf("provision failed after %d attempts: %v", n, cause))
		return err
	}
	return nil
}

// turnFailed distinguishes a lost sandbox (the session cannot continue) from a transient error
// talking to sandboxd (the next worker re-attaches).
func (s *Supervisor) turnFailed(ctx context.Context, ss store.Session, t store.Turn, cause error) error {
	lost, err := s.Ops.IsLost(ctx, ss)
	if err != nil {
		return fmt.Errorf("turn %d: %v; checking sandbox: %w", t.Seq, cause, err)
	}
	if !lost {
		return fmt.Errorf("turn %d: %w", t.Seq, cause)
	}
	if _, err := s.Store.FinishTurn(ctx, ss.ID, t.Seq, session.TurnFailed, "", "sandbox lost", nil); err != nil {
		return err
	}
	_, err = s.Store.Fail(ctx, ss.ID, "sandbox lost")
	return err
}

func backoff(attempts int) time.Duration {
	return time.Duration(1<<min(attempts, 5)) * 2 * time.Second
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
