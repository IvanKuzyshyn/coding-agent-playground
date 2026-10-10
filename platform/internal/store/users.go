package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// EnsureUser creates the user on first login. The hash is refreshed on later logins so tokens
// keep working if the server key that derives them changes.
func (s *Store) EnsureUser(ctx context.Context, name string, tokenHash []byte) (created bool, err error) {
	err = s.pool.QueryRow(ctx, `
		INSERT INTO users (name, token_hash) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET token_hash = EXCLUDED.token_hash
		RETURNING xmax = 0`, name, tokenHash).Scan(&created)
	return created, err
}

// UserByTokenHash returns the name of the user whose API token hashes to hash.
func (s *Store) UserByTokenHash(ctx context.Context, hash []byte) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM users WHERE token_hash = $1`, hash).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
