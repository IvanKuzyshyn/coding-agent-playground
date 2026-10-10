// Package api is the HTTP control surface (docs/contracts/api.md). Handlers only read and write
// the store; the supervisor performs every sandbox action.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"terrarium/internal/catalog"
	"terrarium/internal/credentials"
	"terrarium/internal/store"
)

const (
	maxPromptBytes = 100 << 10
	maxNameRunes   = 120
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

type Server struct {
	Store      *store.Store
	Catalog    *catalog.Catalog
	SessionKey []byte
	MaxActive  int
	Log        *slog.Logger
	// EventPoll is how often an SSE stream checks for new events.
	EventPoll time.Duration
	// DenyNet is the sandbox network. The server is attached to it to reach sandboxd, so the API
	// must refuse anything arriving from there.
	DenyNet *net.IPNet
	// WebURL is the web UI's base URL, for links returned over MCP.
	WebURL string
	// LoginHosts are the host names POST /login answers on ("localhost" includes loopback IPs).
	LoginHosts []string
}

type route struct {
	pattern string
	public  bool // served without a token
	handler http.HandlerFunc
}

// restRoutes are the REST endpoints; each must be described in openapi.yaml (openapi_test.go).
func (s *Server) restRoutes() []route {
	return []route{
		{"GET /healthz", true, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}},
		{"POST /login", true, s.login},
		{"GET /whoami", false, s.whoami},
		{"GET /presets", false, s.presets},
		{"POST /sessions", false, s.createSession},
		{"GET /sessions", false, s.listSessions},
		{"GET /stats", false, s.stats},
		{"GET /sessions/{id}", false, s.getSession},
		{"DELETE /sessions/{id}", false, s.deleteSession},
		{"POST /sessions/{id}/turns", false, s.createTurn},
		{"POST /sessions/{id}/turns/current/stop", false, s.stopTurn},
		{"GET /sessions/{id}/events", false, s.events},
	}
}

func (s *Server) Handler() http.Handler {
	authed := http.NewServeMux()
	mux := http.NewServeMux()
	for _, rt := range s.restRoutes() {
		if rt.public {
			mux.HandleFunc(rt.pattern, rt.handler)
		} else {
			authed.HandleFunc(rt.pattern, rt.handler)
		}
	}
	mux.HandleFunc("GET /openapi.yaml", serveSpec)
	mux.HandleFunc("GET /docs", serveDocs)
	mux.Handle("/mcp", s.mcpHandler())
	mux.Handle("/", s.middleware(authed))
	return s.denySandboxes(mux)
}

func (s *Server) denySandboxes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if s.DenyNet != nil && (err != nil || ip == nil || s.DenyNet.Contains(ip)) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"name": owner(r.Context())})
}

func (s *Server) presets(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]string{}
	for _, n := range s.Catalog.Names() {
		out = append(out, map[string]string{"name": n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

// clientError is a request the caller must fix; status is what the REST API answers with. The
// REST and MCP surfaces share the operations below so their rules cannot drift apart.
type clientError struct {
	status int
	msg    string
}

func (e *clientError) Error() string { return e.msg }

func badRequest(msg string) error { return &clientError{http.StatusBadRequest, msg} }

var errSessionNotFound = &clientError{http.StatusNotFound, "session not found"}

type createRequest struct {
	Name     string `json:"name"`
	Harness  string `json:"harness"`
	Prompt   string `json:"prompt"`
	Repo     string `json:"repo"`
	Preset   string `json:"preset"`
	Model    string `json:"model"`
	KeepIdle bool   `json:"keep_idle"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	id, err := s.create(r.Context(), owner(r.Context()), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeSession(w, r, id, http.StatusAccepted, false)
}

func (s *Server) create(ctx context.Context, who string, req createRequest) (uuid.UUID, error) {
	req.Name = strings.Join(strings.Fields(req.Name), " ")
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxNameRunes {
		return uuid.Nil, badRequest(fmt.Sprintf("name is required (1-%d characters)", maxNameRunes))
	}
	if msg := validatePrompt(req.Prompt); msg != "" {
		return uuid.Nil, badRequest(msg)
	}
	if req.Repo != "" && !repoPattern.MatchString(req.Repo) {
		return uuid.Nil, badRequest("repo must be owner/name")
	}
	spec, err := s.Catalog.Resolve(catalog.Request{Harness: req.Harness, Preset: req.Preset, Repo: req.Repo, Model: req.Model, KeepIdle: req.KeepIdle})
	if err != nil {
		return uuid.Nil, badRequest(err.Error())
	}
	id := uuid.New()
	err = s.Store.CreateSession(ctx, store.NewSession{
		ID:                id,
		Owner:             who,
		Name:              req.Name,
		Spec:              spec,
		SandboxToken:      randomHex(32),
		GatewaySecretHash: credentials.Hash(credentials.DeriveSecret(s.SessionKey, id.String())),
		ExpiresAt:         time.Now().Add(time.Duration(spec.Limits.TTLSeconds) * time.Second),
		Prompt:            req.Prompt,
		MaxActive:         s.MaxActive,
	})
	if errors.Is(err, store.ErrLimit) {
		return uuid.Nil, &clientError{http.StatusTooManyRequests, fmt.Sprintf("active session limit (%d) reached", s.MaxActive)}
	}
	return id, err
}

var (
	displayStatuses = map[string]bool{"pending": true, "provisioning": true, "idle": true, "running": true, "stopping": true, "complete": true, "failed": true, "stopped": true}
	harnesses       = map[string]bool{"claude-code": true, "codex": true}
)

// listFilter parses the listing parameters /sessions and /stats share. Sessions of every user
// are included unless owner is given.
func listFilter(q url.Values) (store.ListFilter, error) {
	f := store.ListFilter{Owner: q.Get("owner"), Query: q.Get("q"), Sort: "created_at", Desc: true, Limit: 50}
	var bad string
	if f.Statuses, bad = csvParam(q.Get("status"), displayStatuses); bad != "" {
		return f, badRequest("unknown status " + strconv.Quote(bad))
	}
	if f.Harnesses, bad = csvParam(q.Get("harness"), harnesses); bad != "" {
		return f, badRequest("unknown harness " + strconv.Quote(bad))
	}
	if v := q.Get("sort"); v != "" {
		if !store.ValidSort(v) {
			return f, badRequest("unknown sort " + strconv.Quote(v))
		}
		f.Sort = v
	}
	switch q.Get("order") {
	case "", "desc":
	case "asc":
		f.Desc = false
	default:
		return f, badRequest("order must be asc or desc")
	}
	var err error
	if f.Limit, err = intParam(q.Get("limit"), 50, 1, 200); err != nil {
		return f, badRequest("limit: " + err.Error())
	}
	if f.Offset, err = intParam(q.Get("offset"), 0, 0, 1<<30); err != nil {
		return f, badRequest("offset: " + err.Error())
	}
	return f, nil
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	f, err := listFilter(r.URL.Query())
	if err != nil {
		s.fail(w, err)
		return
	}
	list, total, err := s.Store.ListSessions(r.Context(), f)
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, ss := range list {
		out = append(out, toSessionJSON(ss, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "total": total})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	f, err := listFilter(r.URL.Query())
	if err != nil {
		s.fail(w, err)
		return
	}
	st, err := s.Store.Stats(r.Context(), f)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// csvParam splits a comma-separated parameter and returns the first value not in allowed.
func csvParam(v string, allowed map[string]bool) ([]string, string) {
	if v == "" {
		return nil, ""
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if !allowed[p] {
			return nil, p
		}
		out = append(out, p)
	}
	return out, ""
}

func intParam(v string, def, lo, hi int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("must be an integer in [%d, %d]", lo, hi)
	}
	return n, nil
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.visibleID(w, r)
	if !ok {
		return
	}
	s.writeSession(w, r, id, http.StatusOK, true)
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedID(w, r)
	if !ok {
		return
	}
	if err := s.end(r.Context(), id, "deleted by "+owner(r.Context())); err != nil {
		s.internal(w, err)
		return
	}
	s.writeSession(w, r, id, http.StatusAccepted, false)
}

// end asks the supervisor to destroy the sandbox. The final status comes from the last finished
// turn (complete, failed or stopped).
func (s *Server) end(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.Store.EndSession(ctx, id, "", reason, "")
	return err
}

func (s *Server) createTurn(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedID(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &req) {
		return
	}
	t, err := s.enqueue(r.Context(), id, req.Prompt)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTurnJSON(t))
}

func (s *Server) enqueue(ctx context.Context, id uuid.UUID, prompt string) (store.Turn, error) {
	if msg := validatePrompt(prompt); msg != "" {
		return store.Turn{}, badRequest(msg)
	}
	t, err := s.Store.EnqueueTurn(ctx, id, prompt)
	if errors.Is(err, store.ErrConflict) {
		return t, &clientError{http.StatusConflict, "session is not accepting turns"}
	}
	return t, err
}

func (s *Server) stopTurn(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedID(w, r)
	if !ok {
		return
	}
	changed, err := s.Store.RequestStopCurrentTurn(r.Context(), id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if !changed {
		writeError(w, http.StatusConflict, "no running turn")
		return
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}

// visibleID parses the path id of a session to read. Every user can read every session.
func (s *Server) visibleID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	ss, err := s.find(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return uuid.Nil, false
	}
	return ss.ID, true
}

// ownedID parses the path id of a session to change; only its owner may.
func (s *Server) ownedID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	ss, err := s.owned(r.Context(), owner(r.Context()), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return uuid.Nil, false
	}
	return ss.ID, true
}

func (s *Server) find(ctx context.Context, rawID string) (store.Session, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return store.Session{}, errSessionNotFound
	}
	ss, err := s.Store.GetSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, errSessionNotFound
	}
	return ss, err
}

// owned returns the session if who started it; others may read it but not change it.
func (s *Server) owned(ctx context.Context, who, rawID string) (store.Session, error) {
	ss, err := s.find(ctx, rawID)
	if err == nil && ss.Owner != who {
		return store.Session{}, &clientError{http.StatusForbidden, "only " + ss.Owner + ", who started this session, can change it"}
	}
	return ss, err
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int, withTurns bool) {
	ss, err := s.Store.GetSession(r.Context(), id)
	if err != nil {
		s.internal(w, err)
		return
	}
	var turns []store.Turn
	if withTurns {
		if turns, err = s.Store.Turns(r.Context(), id); err != nil {
			s.internal(w, err)
			return
		}
	}
	writeJSON(w, status, toSessionJSON(ss, turns))
}

// fail answers a clientError with its status and anything else as a logged 500.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var ce *clientError
	if errors.As(err, &ce) {
		writeError(w, ce.status, ce.msg)
		return
	}
	s.internal(w, err)
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Log.Error("api", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func validatePrompt(p string) string {
	switch {
	case strings.TrimSpace(p) == "":
		return "prompt is required"
	case len(p) > maxPromptBytes:
		return "prompt too large"
	}
	return ""
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*maxPromptBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func queryInt(r *http.Request, key string, def, max int64) int64 {
	v, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || v < 0 {
		return def
	}
	if max > 0 && v > max {
		return max
	}
	return v
}
