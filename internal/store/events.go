package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event kinds.
const (
	EventLifecycle = "lifecycle"
	EventHarness   = "harness"
	EventStderr    = "stderr"
	EventEgress    = "egress"
	EventSystem    = "system"
)

type Event struct {
	ID        int64           `json:"id"`
	SessionID uuid.UUID       `json:"session_id"`
	Kind      string          `json:"kind"`
	TurnSeq   *int            `json:"turn_seq,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	TS        time.Time       `json:"ts"`
}

// AppendEvent adds one event to a session's stream. payload must marshal to a JSON object.
func (s *Store) AppendEvent(ctx context.Context, sessionID uuid.UUID, kind string, turnSeq *int, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// The gateway does not know turns; attribute its egress events to the running turn, if any.
	_, err = s.pool.Exec(ctx, `
		INSERT INTO session_events (session_id, kind, turn_seq, payload)
		VALUES ($1, $2, coalesce($3, CASE WHEN $2 = 'egress' THEN
			(SELECT seq FROM turns WHERE session_id = $1 AND status = 'running' ORDER BY seq DESC LIMIT 1) END), $4)`,
		sessionID, kind, turnSeq, b)
	return err
}

// Events returns up to limit events with id > after, oldest first.
func (s *Store) Events(ctx context.Context, sessionID uuid.UUID, after int64, kind string, limit int) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, kind, turn_seq, payload, ts FROM session_events
		WHERE session_id = $1 AND id > $2 AND ($3 = '' OR kind = $3)
		ORDER BY id LIMIT $4`, sessionID, after, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Kind, &e.TurnSeq, &e.Payload, &e.TS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
