// Package api is the HTTP control surface (docs/contracts/api.md). Handlers only read and write
// the store; the supervisor performs every sandbox action.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
	Engineers  Engineers
	SessionKey []byte
	MaxActive  int
	Log        *slog.Logger
	// EventPoll is how often an SSE stream checks for new events.
	EventPoll time.Duration
	// DenyNet is the sandbox network. The server is attached to it to reach sandboxd, so the API
	// must refuse anything arriving from there.
	DenyNet *net.IPNet
}

func (s *Server) Handler() http.Handler {
	authed := http.NewServeMux()
	authed.HandleFunc("GET /whoami", s.whoami)
	authed.HandleFunc("GET /presets", s.presets)
	authed.HandleFunc("POST /sessions", s.createSession)
	authed.HandleFunc("GET /sessions", s.listSessions)
	authed.HandleFunc("GET /stats", s.stats)
	authed.HandleFunc("GET /sessions/{id}", s.getSession)
	authed.HandleFunc("DELETE /sessions/{id}", s.deleteSession)
	authed.HandleFunc("POST /sessions/{id}/turns", s.createTurn)
	authed.HandleFunc("POST /sessions/{id}/turns/current/stop", s.stopTurn)
	authed.HandleFunc("GET /sessions/{id}/events", s.events)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.Handle("/", s.Engineers.middleware(authed))
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

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Harness  string `json:"harness"`
		Prompt   string `json:"prompt"`
		Repo     string `json:"repo"`
		Preset   string `json:"preset"`
		Model    string `json:"model"`
		KeepIdle bool   `json:"keep_idle"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.Join(strings.Fields(req.Name), " ")
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxNameRunes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("name is required (1-%d characters)", maxNameRunes))
		return
	}
	if msg := validatePrompt(req.Prompt); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if req.Repo != "" && !repoPattern.MatchString(req.Repo) {
		writeError(w, http.StatusBadRequest, "repo must be owner/name")
		return
	}
	spec, err := s.Catalog.Resolve(catalog.Request{Harness: req.Harness, Preset: req.Preset, Repo: req.Repo, Model: req.Model, KeepIdle: req.KeepIdle})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	who := owner(r.Context())
	id := uuid.New()
	err = s.Store.CreateSession(r.Context(), store.NewSession{
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
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("active session limit (%d) reached", s.MaxActive))
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.writeSession(w, r, id, http.StatusAccepted, false)
}

var (
	displayStatuses = map[string]bool{"pending": true, "provisioning": true, "idle": true, "running": true, "stopping": true, "complete": true, "failed": true, "stopped": true}
	harnesses       = map[string]bool{"claude-code": true, "codex": true}
)

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{Owner: owner(r.Context()), Query: q.Get("q"), Sort: "created_at", Desc: true, Limit: 50}
	var bad string
	if f.Statuses, bad = csvParam(q.Get("status"), displayStatuses); bad != "" {
		writeError(w, http.StatusBadRequest, "unknown status "+strconv.Quote(bad))
		return
	}
	if f.Harnesses, bad = csvParam(q.Get("harness"), harnesses); bad != "" {
		writeError(w, http.StatusBadRequest, "unknown harness "+strconv.Quote(bad))
		return
	}
	if v := q.Get("sort"); v != "" {
		if !store.ValidSort(v) {
			writeError(w, http.StatusBadRequest, "unknown sort "+strconv.Quote(v))
			return
		}
		f.Sort = v
	}
	switch q.Get("order") {
	case "", "desc":
	case "asc":
		f.Desc = false
	default:
		writeError(w, http.StatusBadRequest, "order must be asc or desc")
		return
	}
	var err error
	if f.Limit, err = intParam(q.Get("limit"), 50, 1, 200); err != nil {
		writeError(w, http.StatusBadRequest, "limit: "+err.Error())
		return
	}
	if f.Offset, err = intParam(q.Get("offset"), 0, 0, 1<<30); err != nil {
		writeError(w, http.StatusBadRequest, "offset: "+err.Error())
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
	st, err := s.Store.Stats(r.Context(), owner(r.Context()))
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
	id, ok := s.ownedID(w, r)
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
	// The final status comes from the last finished turn (complete, failed or stopped).
	if _, err := s.Store.EndSession(r.Context(), id, "", "deleted by "+owner(r.Context()), ""); err != nil {
		s.internal(w, err)
		return
	}
	s.writeSession(w, r, id, http.StatusAccepted, false)
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
	if msg := validatePrompt(req.Prompt); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	t, err := s.Store.EnqueueTurn(r.Context(), id, req.Prompt)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "session is not accepting turns")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toTurnJSON(t))
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

// ownedID parses the path id and hides sessions owned by someone else behind a 404.
func (s *Server) ownedID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return id, false
	}
	ss, err := s.Store.GetSession(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && ss.Owner != owner(r.Context())) {
		writeError(w, http.StatusNotFound, "session not found")
		return id, false
	}
	if err != nil {
		s.internal(w, err)
		return id, false
	}
	return id, true
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
