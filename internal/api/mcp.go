package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"terrarium/internal/catalog"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// MCP surface (docs/contracts/mcp.md): the same operations as the REST API, as tools another
// agent can call. Stateless Streamable HTTP, authenticated with the engineer's API token.

const (
	defaultResultChars = 4000
	defaultWait        = 120 * time.Second
	maxWait            = 600 * time.Second
	recentEvents       = 25
)

var mcpWaitPoll = time.Second

func (s *Server) mcpHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "terrarium", Version: "v1"}, &mcp.ServerOptions{
		Instructions: "Terrarium runs coding agents (Claude Code or Codex) in disposable sandboxes. " +
			"Call list_options first, create_session to start one, wait_for_session until it settles, " +
			"then get_session for turns and results. Sessions end by themselves when the agent finishes " +
			"unless keep_idle is set; terminate_session ends one early.",
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	notDestructive := false
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_options",
		Description: "List the harnesses and presets create_session accepts, with each preset's limits, allowed egress hosts and lifecycle rules.",
		Annotations: readOnly,
	}, s.mcpListOptions)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_session",
		Description: "Start a sandboxed agent session: a fresh sandbox, optionally with a public GitHub repo cloned, " +
			"runs the prompt with the chosen harness. Returns immediately; use wait_for_session to follow it.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.mcpCreate)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_session",
		Description: "Get a session: status, error, cost, tokens, duration and its turns (prompt, status, result, error, usage).",
		Annotations: readOnly,
	}, s.mcpGet)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "wait_for_session",
		Description: "Wait until the session ends or its current turn finishes (idle with nothing queued), " +
			"or the timeout passes. Returns the session like get_session, with timed_out set on timeout.",
		Annotations: readOnly,
	}, s.mcpWait)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_sessions",
		Description: "List your sessions, newest first, optionally filtered by status, harness or name substring.",
		Annotations: readOnly,
	}, s.mcpList)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "send_follow_up",
		Description: "Queue a follow-up prompt on a session that has not ended. It runs after the current turn, " +
			"resuming the agent's conversation. A session only stays open after its first turn if created with keep_idle.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &notDestructive},
	}, s.mcpFollowUp)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "terminate_session",
		Description: "End a session and destroy its sandbox. A running turn is cancelled. Safe to repeat.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, s.mcpTerminate)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return auth.RequireBearerToken(s.Engineers.verifyMCP, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
}

// verifyMCP maps the engineer API token to the engineer, the same check as the REST middleware.
func (e Engineers) verifyMCP(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	sum := sha256.Sum256([]byte(token))
	name, ok := e[hex.EncodeToString(sum[:])]
	if !ok {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: name}, nil
}

func mcpOwner(req *mcp.CallToolRequest) string { return req.Extra.TokenInfo.UserID }

// mcpErr turns client errors into tool errors the calling agent can act on and hides the rest.
func (s *Server) mcpErr(err error) error {
	var ce *clientError
	if errors.As(err, &ce) {
		return ce
	}
	s.Log.Error("mcp", "err", err)
	return errors.New("internal error")
}

// ---- outputs ----

type mcpTurn struct {
	Seq       int            `json:"seq"`
	Prompt    string         `json:"prompt"`
	Status    string         `json:"status"`
	Result    string         `json:"result,omitempty"`
	Truncated bool           `json:"truncated,omitempty" jsonschema:"result was cut to max_result_chars"`
	Error     string         `json:"error,omitempty"`
	Usage     map[string]any `json:"usage,omitempty"`
	StartedAt *time.Time     `json:"started_at,omitempty"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
}

type mcpEvent struct {
	Kind    string         `json:"kind"`
	TurnSeq *int           `json:"turn_seq,omitempty"`
	Payload map[string]any `json:"payload"`
	TS      time.Time      `json:"ts"`
}

type mcpSession struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Status          string            `json:"status" jsonschema:"pending, provisioning, idle, running, stopping, complete, failed or stopped"`
	Harness         string            `json:"harness"`
	Preset          string            `json:"preset"`
	Repo            string            `json:"repo,omitempty"`
	Model           string            `json:"model,omitempty"`
	Lifecycle       session.Lifecycle `json:"lifecycle"`
	Error           string            `json:"error,omitempty"`
	TokensUsed      int64             `json:"tokens_used"`
	TokenBudget     int64             `json:"token_budget"`
	CostUSD         float64           `json:"cost_usd" jsonschema:"API-equivalent cost estimate"`
	DurationSeconds int64             `json:"duration_seconds"`
	CreatedAt       time.Time         `json:"created_at"`
	EndedAt         *time.Time        `json:"ended_at,omitempty"`
	TurnCount       int               `json:"turn_count"`
	CurrentTurn     *int              `json:"current_turn,omitempty"`
	UIURL           string            `json:"ui_url" jsonschema:"the session in the Terrarium web UI"`
	Turns           []mcpTurn         `json:"turns,omitempty"`
	Events          []mcpEvent        `json:"events,omitempty" jsonschema:"recent lifecycle and egress events, if requested"`
	TimedOut        bool              `json:"timed_out,omitempty"`
}

func (s *Server) mcpView(ss store.Session, turns []store.Turn, maxChars int) mcpSession {
	out := mcpSession{
		ID: ss.ID.String(), Name: ss.Name, Status: ss.Status, Harness: ss.Harness, Preset: ss.Spec.Preset,
		Repo: ss.Spec.Repo, Model: ss.Spec.Model, Lifecycle: ss.Spec.Lifecycle, Error: ss.Error,
		TokensUsed: ss.TokensUsed, TokenBudget: ss.Spec.Limits.TokenBudget, CostUSD: ss.CostUSD,
		DurationSeconds: duration(ss), CreatedAt: ss.CreatedAt, EndedAt: ss.EndedAt,
		TurnCount: ss.TurnCount, CurrentTurn: ss.CurrentTurn,
		UIURL: s.WebURL + "/sessions/" + ss.ID.String(),
	}
	for _, t := range turns {
		mt := mcpTurn{Seq: t.Seq, Prompt: t.Prompt, Status: t.Status, Result: t.Result, Error: t.Error,
			StartedAt: t.StartedAt, EndedAt: t.EndedAt}
		if r := []rune(mt.Result); maxChars > 0 && len(r) > maxChars {
			mt.Result, mt.Truncated = string(r[:maxChars]), true
		}
		_ = json.Unmarshal(t.Usage, &mt.Usage)
		out.Turns = append(out.Turns, mt)
	}
	return out
}

// ---- tools ----

type mcpPreset struct {
	Name          string            `json:"name"`
	AllowedHosts  []string          `json:"allowed_hosts" jsonschema:"egress hosts reachable on port 443; GitHub access to the session repo is separate"`
	MaxAgentSteps int               `json:"max_agent_steps"`
	TokenBudget   int64             `json:"token_budget"`
	IdleTimeout   int               `json:"idle_timeout_seconds"`
	TTL           int               `json:"ttl_seconds"`
	Lifecycle     session.Lifecycle `json:"lifecycle"`
}

type listOptionsOut struct {
	Harnesses []string    `json:"harnesses"`
	Presets   []mcpPreset `json:"presets"`
}

func (s *Server) mcpListOptions(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOptionsOut, error) {
	out := listOptionsOut{Harnesses: []string{session.HarnessClaude, session.HarnessCodex}}
	for _, n := range s.Catalog.Names() {
		spec, err := s.Catalog.Resolve(catalog.Request{Harness: session.HarnessClaude, Preset: n})
		if err != nil {
			return nil, out, s.mcpErr(err)
		}
		out.Presets = append(out.Presets, mcpPreset{
			Name: n, AllowedHosts: append([]string{}, spec.Network.Allow...),
			MaxAgentSteps: spec.Limits.MaxAgentSteps, TokenBudget: spec.Limits.TokenBudget,
			IdleTimeout: spec.Limits.IdleTimeoutSeconds, TTL: spec.Limits.TTLSeconds, Lifecycle: spec.Lifecycle,
		})
	}
	return nil, out, nil
}

type createIn struct {
	Name     string `json:"name" jsonschema:"short human-readable name; need not be unique"`
	Harness  string `json:"harness" jsonschema:"claude-code or codex"`
	Prompt   string `json:"prompt" jsonschema:"the task for the agent"`
	Repo     string `json:"repo,omitempty" jsonschema:"public GitHub repository owner/name to clone into the sandbox"`
	Preset   string `json:"preset,omitempty" jsonschema:"preset from list_options; default 'default'"`
	Model    string `json:"model,omitempty" jsonschema:"model override for the harness"`
	KeepIdle bool   `json:"keep_idle,omitempty" jsonschema:"keep the sandbox for follow-ups after each turn instead of ending the session"`
}

func (s *Server) mcpCreate(ctx context.Context, req *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, mcpSession, error) {
	id, err := s.create(ctx, mcpOwner(req), createRequest(in))
	if err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	ss, err := s.Store.GetSession(ctx, id)
	if err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	return nil, s.mcpView(ss, nil, 0), nil
}

type getIn struct {
	ID             string `json:"id"`
	IncludeEvents  bool   `json:"include_events,omitempty" jsonschema:"also return recent lifecycle and egress events"`
	MaxResultChars int    `json:"max_result_chars,omitempty" jsonschema:"cut each turn result to this many characters; default 4000, 0 for the default"`
}

func (s *Server) mcpGet(ctx context.Context, req *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, mcpSession, error) {
	ss, err := s.owned(ctx, mcpOwner(req), in.ID)
	if err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	out, err := s.mcpFull(ctx, ss, in)
	return nil, out, err
}

func (s *Server) mcpFull(ctx context.Context, ss store.Session, in getIn) (mcpSession, error) {
	turns, err := s.Store.Turns(ctx, ss.ID)
	if err != nil {
		return mcpSession{}, s.mcpErr(err)
	}
	maxChars := in.MaxResultChars
	if maxChars <= 0 {
		maxChars = defaultResultChars
	}
	out := s.mcpView(ss, turns, maxChars)
	if in.IncludeEvents {
		for _, kind := range []string{store.EventLifecycle, store.EventEgress} {
			evs, err := s.Store.Events(ctx, ss.ID, 0, kind, 1000)
			if err != nil {
				return mcpSession{}, s.mcpErr(err)
			}
			for _, e := range evs[max(len(evs)-recentEvents, 0):] {
				me := mcpEvent{Kind: e.Kind, TurnSeq: e.TurnSeq, TS: e.TS}
				_ = json.Unmarshal(e.Payload, &me.Payload)
				out.Events = append(out.Events, me)
			}
		}
		slices.SortStableFunc(out.Events, func(a, b mcpEvent) int { return a.TS.Compare(b.TS) })
	}
	return out, nil
}

type waitIn struct {
	ID             string `json:"id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"default 120, max 600"`
	MaxResultChars int    `json:"max_result_chars,omitempty" jsonschema:"as in get_session"`
}

// settled: nothing more will happen without the caller (ended, or idle with no turn queued).
func settled(ss store.Session) bool {
	return session.Terminal(ss.Status) || (ss.Status == session.StatusIdle && ss.CurrentTurn == nil)
}

func (s *Server) mcpWait(ctx context.Context, req *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, mcpSession, error) {
	who := mcpOwner(req)
	timeout := defaultWait
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, maxWait)
	}
	deadline := time.Now().Add(timeout)
	for {
		ss, err := s.owned(ctx, who, in.ID)
		if err != nil {
			return nil, mcpSession{}, s.mcpErr(err)
		}
		done := settled(ss)
		if done || !time.Now().Before(deadline) {
			out, err := s.mcpFull(ctx, ss, getIn{ID: in.ID, MaxResultChars: in.MaxResultChars})
			out.TimedOut = !done
			return nil, out, err
		}
		select {
		case <-ctx.Done():
			return nil, mcpSession{}, ctx.Err()
		case <-time.After(mcpWaitPoll):
		}
	}
}

type listIn struct {
	Status  []string `json:"status,omitempty" jsonschema:"only these statuses"`
	Harness []string `json:"harness,omitempty" jsonschema:"only these harnesses"`
	Query   string   `json:"query,omitempty" jsonschema:"case-insensitive substring of the name"`
	Limit   int      `json:"limit,omitempty" jsonschema:"default 20, max 200"`
}

type listOut struct {
	Sessions []mcpSession `json:"sessions"`
	Total    int          `json:"total" jsonschema:"sessions matching the filters"`
}

func (s *Server) mcpList(ctx context.Context, req *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	for _, st := range in.Status {
		if !displayStatuses[st] {
			return nil, listOut{}, badRequest(fmt.Sprintf("unknown status %q", st))
		}
	}
	for _, h := range in.Harness {
		if !harnesses[h] {
			return nil, listOut{}, badRequest(fmt.Sprintf("unknown harness %q", h))
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	list, total, err := s.Store.ListSessions(ctx, store.ListFilter{
		Owner: mcpOwner(req), Query: in.Query, Statuses: in.Status, Harnesses: in.Harness,
		Sort: "created_at", Desc: true, Limit: min(limit, 200),
	})
	if err != nil {
		return nil, listOut{}, s.mcpErr(err)
	}
	out := listOut{Sessions: []mcpSession{}, Total: total}
	for _, ss := range list {
		out.Sessions = append(out.Sessions, s.mcpView(ss, nil, 0))
	}
	return nil, out, nil
}

type followUpIn struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

type followUpOut struct {
	Seq    int    `json:"seq" jsonschema:"the queued turn's number"`
	Status string `json:"status"`
}

func (s *Server) mcpFollowUp(ctx context.Context, req *mcp.CallToolRequest, in followUpIn) (*mcp.CallToolResult, followUpOut, error) {
	ss, err := s.owned(ctx, mcpOwner(req), in.ID)
	if err != nil {
		return nil, followUpOut{}, s.mcpErr(err)
	}
	t, err := s.enqueue(ctx, ss.ID, in.Prompt)
	if err != nil {
		return nil, followUpOut{}, s.mcpErr(err)
	}
	return nil, followUpOut{Seq: t.Seq, Status: t.Status}, nil
}

type idIn struct {
	ID string `json:"id"`
}

func (s *Server) mcpTerminate(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, mcpSession, error) {
	who := mcpOwner(req)
	ss, err := s.owned(ctx, who, in.ID)
	if err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	if err := s.end(ctx, ss.ID, "terminated by "+who+" via MCP"); err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	if ss, err = s.Store.GetSession(ctx, ss.ID); err != nil {
		return nil, mcpSession{}, s.mcpErr(err)
	}
	return nil, s.mcpView(ss, nil, 0), nil
}
