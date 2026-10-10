package sandboxclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/gomega"
)

// End-to-end behaviour against the real daemon is tested in cmd/sandboxd; this covers the
// client's own request building and error mapping.
func TestClientRequestsAndErrors(t *testing.T) {
	g := NewWithT(t)
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

	g.Expect(c.PutFile(ctx, "/a b/c", []byte("x"), 0o640)).To(Succeed(), "PutFile")
	data, next, state, err := c.Logs(ctx, "c1", "stderr", 7)
	g.Expect(err).NotTo(HaveOccurred(), "Logs")
	g.Expect(string(data)).To(Equal("abc"))
	g.Expect(next).To(Equal(int64(10)))
	g.Expect(state).To(Equal(StateExited))
	g.Expect(c.Signal(ctx, "c1", "TERM")).To(MatchError(ErrExited), "Signal 409")
	_, err = c.Command(ctx, "missing")
	g.Expect(err).To(MatchError(ErrNotFound), "Command 404")
	_, err = c.GetFile(ctx, "/missing")
	g.Expect(err).To(MatchError(ErrNotFound), "GetFile 404")
	_, err = c.Command(ctx, "broken")
	g.Expect(err).To(MatchError(And(ContainSubstring("500"), ContainSubstring("boom"))), "Command 500")
	g.Expect(New(ts.URL, "wrong", nil).Health(ctx)).To(MatchError(ContainSubstring("401")), "Health 401")
}
