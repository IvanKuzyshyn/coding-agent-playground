package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Engineers maps sha256(token) to an engineer name. Only hashes are stored on disk.
type Engineers map[string]string

func LoadEngineers(path string) (Engineers, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Engineers []struct {
			Name        string `yaml:"name"`
			TokenSHA256 string `yaml:"token_sha256"`
		} `yaml:"engineers"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := Engineers{}
	for _, e := range f.Engineers {
		h := strings.ToLower(strings.TrimSpace(e.TokenSHA256))
		if e.Name == "" || len(h) != 64 {
			return nil, fmt.Errorf("%s: invalid entry for %q", path, e.Name)
		}
		out[h] = e.Name
	}
	return out, nil
}

type ctxKey struct{}

func owner(ctx context.Context) string { return ctx.Value(ctxKey{}).(string) }

func (e Engineers) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		// Map lookup on the hash: the comparison never touches the token itself.
		sum := sha256.Sum256([]byte(token))
		name, ok := e[hex.EncodeToString(sum[:])]
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, name)))
	})
}
