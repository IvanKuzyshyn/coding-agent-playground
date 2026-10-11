package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"terrarium/internal/credentials"
	"terrarium/internal/store"
)

var userNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

type ctxKey struct{}

func owner(ctx context.Context) string { return ctx.Value(ctxKey{}).(string) }

// userForToken resolves an API token to its user. Only the token's hash is stored.
func (s *Server) userForToken(ctx context.Context, token string) (string, error) {
	return s.Store.UserByTokenHash(ctx, credentials.Hash(token))
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		name, err := s.userForToken(r.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			s.internal(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, name)))
	})
}

// verifyMCP is the MCP form of the same token check.
func (s *Server) verifyMCP(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	name, err := s.userForToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return nil, auth.ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	return &auth.TokenInfo{UserID: name}, nil
}

// login creates the user on first use and returns their API token. There is no password, so it
// is only served to requests addressed to an allowed host (default: localhost).
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginHostAllowed(r) {
		writeError(w, http.StatusForbidden, "login is only allowed from "+strings.Join(s.LoginHosts, ", "))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !userNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "name must be 1-39 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit")
		return
	}
	token := credentials.UserToken(s.SessionKey, name)
	created, err := s.Store.EnsureUser(r.Context(), name, credentials.Hash(token))
	if err != nil {
		s.internal(w, err)
		return
	}
	if created {
		s.Log.Info("user created", "name", name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "token": token, "created": created})
}

// loginHostAllowed checks the host the user addressed: X-Forwarded-Host when the web UI forwards
// a browser login, otherwise Host. "localhost" also covers the loopback addresses.
func (s *Server) loginHostAllowed(r *http.Request) bool {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, allowed := range s.LoginHosts {
		if host == allowed || (allowed == "localhost" && slices.Contains([]string{"127.0.0.1", "::1"}, host)) {
			return true
		}
	}
	return false
}
