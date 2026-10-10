package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"terrarium/internal/session"
)

// GatewaySession is what the gateway needs to authorise one request.
type GatewaySession struct {
	ID          uuid.UUID
	Status      string
	Harness     string
	Allow       []string
	Repo        string
	TokenBudget int64
	TokensUsed  int64
}

// GatewaySessionBySecretHash resolves a session from sha256(gateway secret). The gateway must
// still check session.Live(Status) before using any real credential.
func (s *Store) GatewaySessionBySecretHash(ctx context.Context, hash []byte) (GatewaySession, error) {
	var (
		g    GatewaySession
		spec []byte
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, status, harness, spec, tokens_used FROM sessions WHERE gateway_secret_hash = $1`, hash).
		Scan(&g.ID, &g.Status, &g.Harness, &spec, &g.TokensUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	var sp session.Spec
	if err := json.Unmarshal(spec, &sp); err != nil {
		return g, err
	}
	g.Allow = sp.Network.Allow
	g.Repo = sp.Repo
	g.TokenBudget = sp.Limits.TokenBudget
	return g, nil
}

// AddTokensUsed adds n to the session's token counter and returns the new total.
func (s *Store) AddTokensUsed(ctx context.Context, id uuid.UUID, n int64) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`UPDATE sessions SET tokens_used = tokens_used + $2, updated_at = now() WHERE id = $1 RETURNING tokens_used`,
		id, n).Scan(&total)
	return total, err
}
