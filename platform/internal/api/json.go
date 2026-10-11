package api

import (
	"encoding/json"
	"time"

	"terrarium/internal/session"
	"terrarium/internal/store"
)

type sessionJSON struct {
	ID              string            `json:"id"`
	Owner           string            `json:"owner"`
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	DisplayStatus   string            `json:"display_status"`
	Harness         string            `json:"harness"`
	Preset          string            `json:"preset"`
	Lifecycle       session.Lifecycle `json:"lifecycle"`
	Repo            string            `json:"repo,omitempty"`
	Model           string            `json:"model,omitempty"`
	TokensUsed      int64             `json:"tokens_used"`
	TokenBudget     int64             `json:"token_budget"`
	CostUSD         float64           `json:"cost_usd"`
	CostComplete    bool              `json:"cost_complete"`
	DurationSeconds int64             `json:"duration_seconds"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
	IdleUntil       *time.Time        `json:"idle_until,omitempty"`
	EndedAt         *time.Time        `json:"ended_at,omitempty"`
	TurnCount       int               `json:"turn_count"`
	CurrentTurn     *int              `json:"current_turn,omitempty"`
	Turns           []turnJSON        `json:"turns,omitempty"`
}

type turnJSON struct {
	Seq       int             `json:"seq"`
	Prompt    string          `json:"prompt"`
	Status    string          `json:"status"`
	Result    string          `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
}

func toSessionJSON(s store.Session, turns []store.Turn) sessionJSON {
	out := sessionJSON{
		ID: s.ID.String(), Owner: s.Owner, Name: s.Name, Status: s.Status,
		DisplayStatus: store.DisplayStatus(s.Status, s.Error), Harness: s.Harness,
		CostUSD: s.CostUSD, CostComplete: s.CostComplete, DurationSeconds: duration(s),
		Preset: s.Spec.Preset, Lifecycle: s.Spec.Lifecycle, Repo: s.Spec.Repo, Model: s.Spec.Model,
		TokensUsed: s.TokensUsed, TokenBudget: s.Spec.Limits.TokenBudget, Error: s.Error,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ExpiresAt: s.ExpiresAt,
		IdleUntil: s.IdleUntil, EndedAt: s.EndedAt, TurnCount: s.TurnCount, CurrentTurn: s.CurrentTurn,
	}
	for _, t := range turns {
		out.Turns = append(out.Turns, toTurnJSON(t))
	}
	return out
}

func toTurnJSON(t store.Turn) turnJSON {
	return turnJSON{
		Seq: t.Seq, Prompt: t.Prompt, Status: t.Status, Result: t.Result, Error: t.Error,
		Usage: t.Usage, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, EndedAt: t.EndedAt,
	}
}

func duration(s store.Session) int64 {
	end := time.Now()
	if s.EndedAt != nil {
		end = *s.EndedAt
	}
	return int64(end.Sub(s.CreatedAt).Seconds())
}
