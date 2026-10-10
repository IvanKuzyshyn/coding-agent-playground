package api_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"terrarium/internal/api"
)

// The server joins the sandbox network to reach sandboxd; the API must refuse sandbox callers,
// including on unauthenticated endpoints.
func TestRequestsFromSandboxNetworkAreRefused(t *testing.T) {
	_, sandboxNet, _ := net.ParseCIDR("172.31.250.0/24")
	h := (&api.Server{DenyNet: sandboxNet}).Handler()
	for addr, want := range map[string]int{
		"172.31.250.7:51000": http.StatusForbidden,
		"127.0.0.1:51000":    http.StatusOK,
		"garbage":            http.StatusForbidden,
	} {
		t.Run(addr, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = addr
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			NewWithT(t).Expect(rec.Code).To(Equal(want))
		})
	}
}

func TestActiveSessionCapHoldsUnderConcurrency(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond) // MaxActive 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": "test run", "harness": "codex", "prompt": "hi"})
			if code == http.StatusAccepted {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	NewWithT(t).Expect(created).To(Equal(2), "sessions created concurrently, cap is 2")
}
