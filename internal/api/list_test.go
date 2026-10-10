package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/gomega"

	"terrarium/internal/session"
)

func (e env) createNamed(t *testing.T, token, name, harness string) uuid.UUID {
	t.Helper()
	code, body := e.do(t, http.MethodPost, "/sessions", token, map[string]string{"name": name, "harness": harness, "prompt": "hi"})
	if code != http.StatusAccepted {
		t.Fatalf("create %q: %d %v", name, code, body)
	}
	return uuid.MustParse(body["id"].(string))
}

func names(body map[string]any) []string {
	var out []string
	for _, s := range body["sessions"].([]any) {
		out = append(out, s.(map[string]any)["name"].(string))
	}
	return out
}

func TestNameIsRequiredAndNotUnique(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	for _, n := range []string{"", "   ", strings.Repeat("x", 121)} {
		t.Run(fmt.Sprintf("name %q", n), func(t *testing.T) {
			code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": n, "harness": "codex", "prompt": "hi"})
			NewWithT(t).Expect(code).To(Equal(http.StatusBadRequest))
		})
	}
	g := NewWithT(t)
	code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"harness": "codex", "prompt": "hi"})
	g.Expect(code).To(Equal(http.StatusBadRequest), "missing name")
	a := e.createNamed(t, alice, "  same\tname ", "codex")
	b := e.createNamed(t, alice, "same name", "codex")
	g.Expect(a).NotTo(Equal(b), "ids must differ")
	_, body := e.do(t, http.MethodGet, "/sessions/"+a.String(), alice, nil)
	g.Expect(body).To(HaveKeyWithValue("name", "same name"), "name not normalised")
	g.Expect(body).To(And(
		HaveKey("display_status"),
		HaveKey("cost_usd"),
		HaveKey("cost_complete"),
		HaveKey("duration_seconds"),
	))
}

func TestListFilterSortPaginate(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	mk := func(name, harness string) uuid.UUID {
		id := e.createNamed(t, alice, name, harness)
		e.stop(t, id)
		return id
	}
	mk("Fix flaky test", "codex")
	mk("fix login bug", "claude-code")
	failed := mk("Refactor store", "claude-code")
	_, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET status = 'failed', error = 'boom' WHERE id = $1`, failed)
	g.Expect(err).NotTo(HaveOccurred())
	e.createNamed(t, bob, "fix bob's thing", "codex")

	code, body := e.do(t, http.MethodGet, "/sessions?q=FIX", alice, nil)
	g.Expect(code).To(Equal(http.StatusOK), "search across users: %v", body)
	g.Expect(body).To(HaveKeyWithValue("total", float64(3)), "search across users")
	_, body = e.do(t, http.MethodGet, "/sessions?q=FIX&owner=alice", alice, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(2)), "search with owner")
	_, body = e.do(t, http.MethodGet, "/sessions?harness=claude-code&sort=name&order=asc", alice, nil)
	g.Expect(names(body)).To(Equal([]string{"fix login bug", "Refactor store"}), "harness filter + name sort")
	_, body = e.do(t, http.MethodGet, "/sessions?status=failed", alice, nil)
	g.Expect(names(body)).To(Equal([]string{"Refactor store"}), "failed filter")
	_, body = e.do(t, http.MethodGet, "/sessions?status=stopped", alice, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(2)), "stopped excludes failed")
	_, body = e.do(t, http.MethodGet, "/sessions?owner=alice&limit=2&offset=2&sort=created_at&order=asc", alice, nil)
	g.Expect(names(body)).To(Equal([]string{"Refactor store"}), "pagination")
	g.Expect(body).To(HaveKeyWithValue("total", float64(3)), "pagination")
	for _, q := range []string{"status=done", "harness=gpt", "sort=id", "order=up", "limit=0", "limit=201", "offset=-1"} {
		t.Run(q, func(t *testing.T) {
			code, _ := e.do(t, http.MethodGet, "/sessions?"+q, alice, nil)
			NewWithT(t).Expect(code).To(Equal(http.StatusBadRequest))
		})
	}
	// SQL metacharacters in the search are literal.
	_, body = e.do(t, http.MethodGet, "/sessions?q=%25", alice, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(0)), "percent search")
}

func TestStats(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	running := e.createNamed(t, alice, "a", "codex")
	e.setStatus(t, running, []string{session.StatusPending}, session.StatusRunning)
	ok := e.createNamed(t, alice, "b", "codex")
	e.stop(t, ok)
	bad := e.createNamed(t, alice, "c", "codex")
	e.stop(t, bad)
	_, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET status = 'failed', error = 'boom' WHERE id = $1`, bad)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.st.AddUsage(context.Background(), ok, 100, 0.25, true)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = e.st.AddUsage(context.Background(), bad, 100, 0.5, true)
	g.Expect(err).NotTo(HaveOccurred())
	_, body := e.do(t, http.MethodGet, "/stats", alice, nil)
	g.Expect(body).To(HaveKeyWithValue("cost_complete", true))
	want := map[string]float64{"total": 3, "running": 1, "active": 1, "failed": 1, "failure_rate": 0.5, "total_cost_usd": 0.75}
	for k, v := range want {
		g.Expect(body).To(HaveKeyWithValue(k, v))
	}
	// Stats follow the listing filters.
	_, body = e.do(t, http.MethodGet, "/stats", bob, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(3)), "stats for everyone")
	_, body = e.do(t, http.MethodGet, "/stats?owner=bob", bob, nil)
	g.Expect(body).To(HaveKeyWithValue("total", float64(0)), "stats for bob")
	_, body = e.do(t, http.MethodGet, "/stats?status=failed&harness=codex", alice, nil)
	g.Expect(body).To(And(HaveKeyWithValue("total", float64(1)), HaveKeyWithValue("failed", float64(1))), "stats with status and harness filters")
	code, _ := e.do(t, http.MethodGet, "/stats?status=bogus", alice, nil)
	g.Expect(code).To(Equal(http.StatusBadRequest), "stats with a bad filter")
}
