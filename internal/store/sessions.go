package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"terrarium/internal/session"
)

// ErrConflict is returned when a state change is not allowed from the current state.
var ErrConflict = errors.New("conflict")

type Session struct {
	ID             uuid.UUID
	Owner          string
	Name           string
	Status         string
	FinalStatus    string // set while stopping when the final status is already decided
	Harness        string
	Spec           session.Spec
	HarnessSession string
	ContainerID    string
	SandboxToken   string
	TokensUsed     int64
	CostUSD        float64
	CostComplete   bool
	Error          string
	Attempts       int
	NextAttemptAt  *time.Time
	IdleUntil      *time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	EndedAt        *time.Time
	TurnCount      int
	CurrentTurn    *int
}

type Turn struct {
	SessionID     uuid.UUID
	Seq           int
	Prompt        string
	Status        string
	CommandID     string
	LogOffset     int64
	ErrOffset     int64
	StopRequested bool
	Usage         json.RawMessage
	Result        string
	Error         string
	CreatedAt     time.Time
	StartedAt     *time.Time
	EndedAt       *time.Time
}

type NewSession struct {
	ID                uuid.UUID
	Owner             string
	Name              string
	Spec              session.Spec
	SandboxToken      string
	GatewaySecretHash []byte
	ExpiresAt         time.Time
	Prompt            string
	// MaxActive caps the owner's sessions that are not stopped or failed; 0 means no cap.
	MaxActive int
}

// ErrLimit is returned when creating a session would exceed the owner's active-session cap.
var ErrLimit = errors.New("active session limit reached")

const sessionCols = `s.id, s.owner, s.name, s.status, coalesce(s.final_status,''), s.harness, s.spec, coalesce(s.harness_session,''),
	coalesce(s.container_id,''), s.sandbox_token, s.tokens_used, s.cost_usd::float8, s.cost_complete,
	coalesce(s.error,''), s.attempts,
	s.next_attempt_at, s.idle_until, s.expires_at, s.created_at, s.updated_at, s.ended_at,
	(SELECT count(*) FROM turns t WHERE t.session_id = s.id),
	(SELECT min(seq) FROM turns t WHERE t.session_id = s.id AND t.status IN ('queued','running'))`

func scanSession(row pgx.Row) (Session, error) {
	var (
		s    Session
		spec []byte
	)
	err := row.Scan(&s.ID, &s.Owner, &s.Name, &s.Status, &s.FinalStatus, &s.Harness, &spec, &s.HarnessSession, &s.ContainerID,
		&s.SandboxToken, &s.TokensUsed, &s.CostUSD, &s.CostComplete, &s.Error, &s.Attempts, &s.NextAttemptAt, &s.IdleUntil,
		&s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt, &s.EndedAt, &s.TurnCount, &s.CurrentTurn)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(spec, &s.Spec)
}

// CreateSession inserts the session (pending), its first turn (queued) and a lifecycle event.
func (s *Store) CreateSession(ctx context.Context, n NewSession) error {
	spec, err := json.Marshal(n.Spec)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if n.MaxActive > 0 {
			// Serialise creates per owner so concurrent requests cannot all pass the count.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('sessions:' || $1))`, n.Owner); err != nil {
				return err
			}
			var active int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE owner = $1 AND status NOT IN ('complete','failed','stopped')`, n.Owner).Scan(&active); err != nil {
				return err
			}
			if active >= n.MaxActive {
				return ErrLimit
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, owner, name, status, harness, spec, sandbox_token, gateway_secret_hash, expires_at)
			VALUES ($1, $2, $3, 'pending', $4, $5, $6, $7, $8)`,
			n.ID, n.Owner, n.Name, n.Spec.Harness, spec, n.SandboxToken, n.GatewaySecretHash, n.ExpiresAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO turns (session_id, seq, prompt, status) VALUES ($1, 1, $2, 'queued')`,
			n.ID, n.Prompt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO session_events (session_id, kind, payload) VALUES ($1, 'lifecycle', $2)`,
			n.ID, []byte(`{"status":"pending","message":"session created"}`))
		return err
	})
}

func (s *Store) GetSession(ctx context.Context, id uuid.UUID) (Session, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions s WHERE s.id = $1`, id))
}

func (s *Store) listSessions(ctx context.Context, where string, args ...any) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionCols+` FROM sessions s WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Store) ListSessionsByOwner(ctx context.Context, owner string, limit int) ([]Session, error) {
	return s.listSessions(ctx, `s.owner = $1 ORDER BY s.created_at DESC LIMIT $2`, owner, limit)
}

// ActiveSessions returns every session the supervisor still has work for.
func (s *Store) ActiveSessions(ctx context.Context) ([]Session, error) {
	return s.listSessions(ctx, `s.status NOT IN ('complete','failed','stopped') ORDER BY s.created_at`)
}

// SessionIDs returns all session ids with their status, for orphan detection.
func (s *Store) SessionStatuses(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, status FROM sessions WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var (
			id uuid.UUID
			st string
		)
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

func (s *Store) CountActiveByOwner(ctx context.Context, owner string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE owner = $1 AND status NOT IN ('complete','failed','stopped')`, owner).Scan(&n)
	return n, err
}

// SetStatus moves a session to `to` only if its status is one of `from`. It reports whether the
// row changed, so concurrent writers (API, supervisor) never overwrite each other.
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, from []string, to, message string) (bool, error) {
	var changed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE sessions SET status = $3, updated_at = now(),
				ended_at = CASE WHEN $3 IN ('complete','failed','stopped') THEN now() ELSE ended_at END,
				idle_until = CASE WHEN $3 IN ('stopping','complete','failed','stopped') THEN NULL ELSE idle_until END
			WHERE id = $1 AND status = ANY($2)`, id, from, to)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		changed = true
		payload, _ := json.Marshal(map[string]string{"status": to, "message": message})
		_, err = tx.Exec(ctx, `INSERT INTO session_events (session_id, kind, payload) VALUES ($1, 'lifecycle', $2)`, id, payload)
		return err
	})
	return changed, err
}

// liveStatuses are the statuses a session can be ended from.
var liveStatuses = []string{session.StatusPending, session.StatusProvisioning, session.StatusIdle, session.StatusRunning}

// EndSession moves a session to stopping and records the terminal status cleanup will apply
// (empty: derive it from the last finished turn). errMsg, if set, is stored as the session error.
// It reports whether the session was live and is now ending.
func (s *Store) EndSession(ctx context.Context, id uuid.UUID, final, msg, errMsg string) (bool, error) {
	return s.endSession(ctx, id, final, msg, errMsg, false)
}

// EndAfterTurn is EndSession for lifecycle rules: it does nothing if a follow-up turn is queued.
// The row lock serialises it with EnqueueTurn, so a follow-up accepted by the API is never
// cancelled by a concurrent end.
func (s *Store) EndAfterTurn(ctx context.Context, id uuid.UUID, final, msg, errMsg string) (bool, error) {
	return s.endSession(ctx, id, final, msg, errMsg, true)
}

func (s *Store) endSession(ctx context.Context, id uuid.UUID, final, msg, errMsg string, unlessQueued bool) (bool, error) {
	var changed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if unlessQueued {
			if _, err := tx.Exec(ctx, `SELECT 1 FROM sessions WHERE id = $1 FOR UPDATE`, id); err != nil {
				return err
			}
			// A new statement after the lock sees turns committed by a concurrent EnqueueTurn.
			var queued bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM turns WHERE session_id = $1 AND status = 'queued')`, id).Scan(&queued); err != nil {
				return err
			}
			if queued {
				return nil
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE sessions SET status = 'stopping', final_status = nullif($3, ''),
				error = coalesce(nullif($4, ''), error), idle_until = NULL, updated_at = now()
			WHERE id = $1 AND status = ANY($2)`, id, liveStatuses, final, errMsg)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		changed = true
		payload, _ := json.Marshal(map[string]string{"status": session.StatusStopping, "message": msg})
		_, err = tx.Exec(ctx, `INSERT INTO session_events (session_id, kind, payload) VALUES ($1, 'lifecycle', $2)`, id, payload)
		return err
	})
	return changed, err
}

// Fail ends a live session as failed with msg as its error.
func (s *Store) Fail(ctx context.Context, id uuid.UUID, msg string) (bool, error) {
	return s.EndSession(ctx, id, session.StatusFailed, msg, msg)
}

// LastFinishedTurnStatus returns the status of the latest turn that ran to an end, or "".
func (s *Store) LastFinishedTurnStatus(ctx context.Context, id uuid.UUID) (string, error) {
	var st string
	err := s.pool.QueryRow(ctx, `
		SELECT status FROM turns WHERE session_id = $1 AND started_at IS NOT NULL
			AND status IN ('complete','failed','stopped','limit_exceeded')
		ORDER BY seq DESC LIMIT 1`, id).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return st, err
}

// SetContainerID records the cleanup obligation. Called before the container is created.
func (s *Store) SetContainerID(ctx context.Context, id uuid.UUID, containerID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET container_id = $2, updated_at = now() WHERE id = $1`, id, containerID)
	return err
}

func (s *Store) SetHarnessSession(ctx context.Context, id uuid.UUID, hs string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET harness_session = $2 WHERE id = $1 AND harness_session IS NULL`, id, hs)
	return err
}

func (s *Store) SetIdleUntil(ctx context.Context, id uuid.UUID, t *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET idle_until = $2, updated_at = now() WHERE id = $1`, id, t)
	return err
}

// RecordAttemptFailure schedules a retry with the given backoff and returns the attempt count.
func (s *Store) RecordAttemptFailure(ctx context.Context, id uuid.UUID, msg string, backoff time.Duration) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		UPDATE sessions SET attempts = attempts + 1, error = $2, next_attempt_at = now() + $3::interval, updated_at = now()
		WHERE id = $1 RETURNING attempts`, id, msg, backoff.String()).Scan(&n)
	return n, err
}

func (s *Store) ResetAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET attempts = 0, next_attempt_at = NULL, error = NULL WHERE id = $1 AND attempts > 0`, id)
	return err
}
