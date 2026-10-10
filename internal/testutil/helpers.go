package testutil

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/session"
	"terrarium/internal/store"
)

// Harness output captured from the spikes (Claude Code 2.1.296, Codex 0.162.1), trimmed. Same
// samples as internal/harness/harness_test.go.
const (
	ClaudeSessionID = "be35dd33-1b7b-415f-865d-7ad0fe2e22ab"
	ClaudeInit      = `{"type":"system","subtype":"init","cwd":"/home/agent","session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab","tools":["Bash"]}`
	ClaudeMessage   = `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]},"session_id":"be35dd33"}`
	ClaudeResult    = `{"type":"result","subtype":"success","is_error":false,"result":"ok","num_turns":3,"session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab","usage":{"input_tokens":4,"output_tokens":272,"cache_read_input_tokens":100,"cache_creation_input_tokens":10}}`
	ClaudeMaxTurns  = `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":50,"session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab","usage":{}}`

	CodexThreadID = "01a12548-5ca3-7e01-b3c4-f15ef9754926"
	CodexThread   = `{"type":"thread.started","thread_id":"01a12548-5ca3-7e01-b3c4-f15ef9754926"}`
	CodexMessage  = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"41"}}`
	CodexDone     = `{"type":"turn.completed","usage":{"input_tokens":22526,"cached_input_tokens":9600,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0}}`
)

func Logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Spec returns a minimal frozen spec for harness h with generous limits.
func Spec(h string) session.Spec {
	return session.Spec{
		Harness: h,
		Preset:  "default",
		Image:   "terrarium-agent:test",
		Limits: session.Limits{
			MaxAgentSteps:      10,
			TokenBudget:        1_000_000,
			IdleTimeoutSeconds: 3600,
			TTLSeconds:         3600,
		},
	}
}

// CreateSession inserts a pending session with turn 1 queued and returns it.
func CreateSession(t testing.TB, st *store.Store, owner string, spec session.Spec, prompt string) store.Session {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	err := st.CreateSession(ctx, store.NewSession{
		ID:                id,
		Owner:             owner,
		Spec:              spec,
		SandboxToken:      randomHex(16),
		GatewaySecretHash: []byte(randomHex(16)),
		ExpiresAt:         time.Now().Add(time.Duration(spec.Limits.TTLSeconds) * time.Second),
		Prompt:            prompt,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return MustSession(t, st, id)
}

func MustSession(t testing.TB, st *store.Store, id uuid.UUID) store.Session {
	t.Helper()
	s, err := st.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return s
}

func MustTurn(t testing.TB, st *store.Store, id uuid.UUID, seq int) store.Turn {
	t.Helper()
	tn, err := st.GetTurn(context.Background(), id, seq)
	if err != nil {
		t.Fatalf("get turn %d: %v", seq, err)
	}
	return tn
}

func MustEvents(t testing.TB, st *store.Store, id uuid.UUID) []store.Event {
	t.Helper()
	ev, err := st.Events(context.Background(), id, 0, "", 10000)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return ev
}

// Payload decodes an event payload into a string map, dropping non-string values.
func Payload(e store.Event) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(e.Payload, &m)
	return m
}

// Statuses lists the session statuses recorded by lifecycle events, in order.
func Statuses(events []store.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind != store.EventLifecycle {
			continue
		}
		if s, ok := Payload(e)["status"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Eventually polls cond until it holds or the timeout passes.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
