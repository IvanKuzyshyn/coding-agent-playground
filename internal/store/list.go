package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// DisplayStatusSQL is the status engineers see: a session that ended because of an error stays
// "failed" after cleanup moved it to "stopped".
const DisplayStatusSQL = `CASE WHEN s.status = 'failed' OR (s.status = 'stopped' AND s.error IS NOT NULL) THEN 'failed' ELSE s.status END`

// DisplayStatus is the Go twin of DisplayStatusSQL.
func DisplayStatus(status, errMsg string) string {
	if status == "failed" || (status == "stopped" && errMsg != "") {
		return "failed"
	}
	return status
}

// Sort keys accepted by ListSessions, mapped to SQL. Values never come from user input directly.
var sortColumns = map[string]string{
	"created_at": "s.created_at",
	"duration":   "(coalesce(s.ended_at, now()) - s.created_at)",
	"cost":       "s.cost_usd",
	"tokens":     "s.tokens_used",
	"name":       "lower(s.name)",
	// Lifecycle order rather than alphabetical.
	"status": `CASE ` + DisplayStatusSQL + ` WHEN 'pending' THEN 1 WHEN 'provisioning' THEN 2 WHEN 'idle' THEN 3
		WHEN 'running' THEN 4 WHEN 'stopping' THEN 5 WHEN 'stopped' THEN 6 ELSE 7 END`,
}

func ValidSort(key string) bool { _, ok := sortColumns[key]; return ok }

type ListFilter struct {
	Owner     string
	Query     string   // substring of name, case-insensitive
	Statuses  []string // display statuses
	Harnesses []string
	Sort      string // key of sortColumns; default created_at
	Desc      bool
	Limit     int
	Offset    int
}

// ListSessions returns one page of the owner's sessions matching the filter and the total match count.
func (s *Store) ListSessions(ctx context.Context, f ListFilter) ([]Session, int, error) {
	where := []string{"s.owner = $1"}
	args := []any{f.Owner}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		where = append(where, fmt.Sprintf(`s.name ILIKE $%d ESCAPE '\'`, len(args)))
	}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		where = append(where, fmt.Sprintf("%s = ANY($%d)", DisplayStatusSQL, len(args)))
	}
	if len(f.Harnesses) > 0 {
		args = append(args, f.Harnesses)
		where = append(where, fmt.Sprintf("s.harness = ANY($%d)", len(args)))
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions s WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	col, ok := sortColumns[f.Sort]
	if !ok {
		col = sortColumns["created_at"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	args = append(args, f.Limit, f.Offset)
	// The id tie-breaker keeps pages stable when sort values are equal.
	list, err := s.listSessions(ctx, fmt.Sprintf("%s ORDER BY %s %s, s.id %s LIMIT $%d OFFSET $%d",
		cond, col, dir, dir, len(args)-1, len(args)), args...)
	return list, total, err
}

type Stats struct {
	Total       int     `json:"total"`
	Running     int     `json:"running"`
	Active      int     `json:"active"`
	Failed      int     `json:"failed"`
	FailureRate float64 `json:"failure_rate"`
}

func (s *Store) Stats(ctx context.Context, owner string) (Stats, error) {
	var st Stats
	var ended int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
			count(*) FILTER (WHERE s.status = 'running'),
			count(*) FILTER (WHERE s.status <> 'stopped'),
			count(*) FILTER (WHERE `+DisplayStatusSQL+` = 'failed'),
			count(*) FILTER (WHERE s.status IN ('stopped','failed'))
		FROM sessions s WHERE s.owner = $1`, owner).Scan(&st.Total, &st.Running, &st.Active, &st.Failed, &ended)
	if err != nil {
		return st, err
	}
	if ended > 0 {
		st.FailureRate = float64(st.Failed) / float64(ended)
	}
	return st, nil
}

// AddUsage records one upstream response: tokens against the budget and its cost estimate.
// priced is false when the model was missing from the price table.
func (s *Store) AddUsage(ctx context.Context, id uuid.UUID, tokens int64, costUSD float64, priced bool) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		UPDATE sessions SET tokens_used = tokens_used + $2, cost_usd = cost_usd + $3,
			cost_complete = cost_complete AND $4, updated_at = now()
		WHERE id = $1 RETURNING tokens_used`, id, tokens, costUSD, priced).Scan(&total)
	return total, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
