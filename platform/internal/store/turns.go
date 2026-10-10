package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const turnCols = `session_id, seq, prompt, status, coalesce(command_id,''), log_offset, err_offset,
	stop_requested, usage, coalesce(result,''), coalesce(error,''), created_at, started_at, ended_at`

func scanTurn(row pgx.Row) (Turn, error) {
	var t Turn
	err := row.Scan(&t.SessionID, &t.Seq, &t.Prompt, &t.Status, &t.CommandID, &t.LogOffset, &t.ErrOffset,
		&t.StopRequested, &t.Usage, &t.Result, &t.Error, &t.CreatedAt, &t.StartedAt, &t.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) Turns(ctx context.Context, sessionID uuid.UUID) ([]Turn, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+turnCols+` FROM turns WHERE session_id = $1 ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		t, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveTurn returns the running turn, or else the oldest queued turn.
func (s *Store) ActiveTurn(ctx context.Context, sessionID uuid.UUID) (Turn, error) {
	return scanTurn(s.pool.QueryRow(ctx, `SELECT `+turnCols+` FROM turns
		WHERE session_id = $1 AND status IN ('running','queued')
		ORDER BY (status = 'running') DESC, seq LIMIT 1`, sessionID))
}

// EnqueueTurn appends a queued turn if the session can still accept work.
func (s *Store) EnqueueTurn(ctx context.Context, sessionID uuid.UUID, prompt string) (Turn, error) {
	var t Turn
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch status {
		case "pending", "provisioning", "idle", "running":
		default:
			return ErrConflict
		}
		t, err = scanTurn(tx.QueryRow(ctx, `
			INSERT INTO turns (session_id, seq, prompt, status)
			VALUES ($1, (SELECT coalesce(max(seq), 0) + 1 FROM turns WHERE session_id = $1), $2, 'queued')
			RETURNING `+turnCols, sessionID, prompt))
		return err
	})
	return t, err
}

// StartTurn marks a queued turn running with its command id, and the session running.
func (s *Store) StartTurn(ctx context.Context, sessionID uuid.UUID, seq int, commandID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE turns SET status = 'running', command_id = $3, started_at = now()
		WHERE session_id = $1 AND seq = $2 AND status = 'queued'`, sessionID, seq, commandID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) SetTurnOffsets(ctx context.Context, sessionID uuid.UUID, seq int, out, errOff int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE turns SET log_offset = $3, err_offset = $4 WHERE session_id = $1 AND seq = $2`,
		sessionID, seq, out, errOff)
	return err
}

// FinishTurn writes the terminal state of a running (or queued, when cancelled) turn once.
func (s *Store) FinishTurn(ctx context.Context, sessionID uuid.UUID, seq int, status, result, errMsg string, usage any) (bool, error) {
	var u []byte
	if usage != nil {
		var err error
		if u, err = json.Marshal(usage); err != nil {
			return false, err
		}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE turns SET status = $3, result = nullif($4,''), error = nullif($5,''), usage = $6, ended_at = now()
		WHERE session_id = $1 AND seq = $2 AND status IN ('running','queued')`,
		sessionID, seq, status, result, errMsg, u)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RequestStopCurrentTurn flags the running turn; the supervisor signals the process.
func (s *Store) RequestStopCurrentTurn(ctx context.Context, sessionID uuid.UUID) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE turns SET stop_requested = true WHERE session_id = $1 AND status = 'running'`, sessionID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CancelQueuedTurns ends turns that will never run because the session is ending.
func (s *Store) CancelQueuedTurns(ctx context.Context, sessionID uuid.UUID, status, msg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE turns SET status = $2, error = $3, ended_at = now()
		WHERE session_id = $1 AND status IN ('queued','running')`, sessionID, status, msg)
	return err
}

func (s *Store) GetTurn(ctx context.Context, sessionID uuid.UUID, seq int) (Turn, error) {
	return scanTurn(s.pool.QueryRow(ctx, `SELECT `+turnCols+` FROM turns WHERE session_id = $1 AND seq = $2`, sessionID, seq))
}
