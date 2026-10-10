package sandboxclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// End-to-end behaviour against the real daemon is tested in cmd/sandboxd; this covers the
// client's own request building and error mapping.
func TestClientRequestsAndErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/files" && r.Method == http.MethodPut:
			if r.URL.Query().Get("mode") != "0640" || r.URL.Query().Get("path") != "/a b/c" {
				http.Error(w, "bad query "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/commands/c1/logs":
			if r.URL.Query().Get("offset") != "7" || r.URL.Query().Get("stream") != "stderr" {
				http.Error(w, "bad query", http.StatusBadRequest)
				return
			}
			w.Header().Set("X-Next-Offset", "10")
			w.Header().Set("X-Command-State", StateExited)
			w.Write([]byte("abc"))
		case r.URL.Path == "/commands/c1/signal":
			w.WriteHeader(http.StatusConflict)
		case r.URL.Path == "/commands/broken":
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	ctx := context.Background()
	c := New(ts.URL+"/", "tok", nil)

	if err := c.PutFile(ctx, "/a b/c", []byte("x"), 0o640); err != nil {
		t.Errorf("PutFile: %v", err)
	}
	data, next, state, err := c.Logs(ctx, "c1", "stderr", 7)
	if err != nil || string(data) != "abc" || next != 10 || state != StateExited {
		t.Errorf("Logs = %q %d %q %v", data, next, state, err)
	}
	if err := c.Signal(ctx, "c1", "TERM"); !errors.Is(err, ErrExited) {
		t.Errorf("Signal 409: %v", err)
	}
	if _, err := c.Command(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Command 404: %v", err)
	}
	if _, err := c.GetFile(ctx, "/missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetFile 404: %v", err)
	}
	if _, err := c.Command(ctx, "broken"); err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Command 500: %v", err)
	}
	if err := New(ts.URL, "wrong", nil).Health(ctx); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("Health 401: %v", err)
	}
}
