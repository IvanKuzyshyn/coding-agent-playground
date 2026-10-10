package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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
		if code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"name": n, "harness": "codex", "prompt": "hi"}); code != http.StatusBadRequest {
			t.Errorf("name %q: %d", n, code)
		}
	}
	if code, _ := e.do(t, http.MethodPost, "/sessions", alice, map[string]string{"harness": "codex", "prompt": "hi"}); code != http.StatusBadRequest {
		t.Errorf("missing name: %d", code)
	}
	a := e.createNamed(t, alice, "  same\tname ", "codex")
	b := e.createNamed(t, alice, "same name", "codex")
	if a == b {
		t.Fatal("ids must differ")
	}
	_, body := e.do(t, http.MethodGet, "/sessions/"+a.String(), alice, nil)
	if body["name"] != "same name" {
		t.Errorf("name not normalised: %q", body["name"])
	}
	for _, f := range []string{"display_status", "cost_usd", "cost_complete", "duration_seconds"} {
		if _, ok := body[f]; !ok {
			t.Errorf("missing field %s", f)
		}
	}
}

func TestListFilterSortPaginate(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	mk := func(name, harness string) uuid.UUID {
		id := e.createNamed(t, alice, name, harness)
		e.stop(t, id)
		return id
	}
	mk("Fix flaky test", "codex")
	mk("fix login bug", "claude-code")
	failed := mk("Refactor store", "claude-code")
	if _, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET error = 'boom' WHERE id = $1`, failed); err != nil {
		t.Fatal(err)
	}
	e.createNamed(t, bob, "fix bob's thing", "codex") // other owner, never listed

	code, body := e.do(t, http.MethodGet, "/sessions?q=FIX", alice, nil)
	if code != http.StatusOK || body["total"].(float64) != 2 {
		t.Fatalf("search: %d %v", code, body)
	}
	_, body = e.do(t, http.MethodGet, "/sessions?harness=claude-code&sort=name&order=asc", alice, nil)
	if got := names(body); len(got) != 2 || got[0] != "fix login bug" || got[1] != "Refactor store" {
		t.Errorf("harness filter + name sort: %v", got)
	}
	_, body = e.do(t, http.MethodGet, "/sessions?status=failed", alice, nil)
	if got := names(body); len(got) != 1 || got[0] != "Refactor store" {
		t.Errorf("failed filter: %v", got)
	}
	_, body = e.do(t, http.MethodGet, "/sessions?status=stopped", alice, nil)
	if body["total"].(float64) != 2 {
		t.Errorf("stopped excludes failed: %v", body["total"])
	}
	_, body = e.do(t, http.MethodGet, "/sessions?limit=2&offset=2&sort=created_at&order=asc", alice, nil)
	if got := names(body); len(got) != 1 || got[0] != "Refactor store" || body["total"].(float64) != 3 {
		t.Errorf("pagination: %v total %v", got, body["total"])
	}
	for _, q := range []string{"status=done", "harness=gpt", "sort=id", "order=up", "limit=0", "limit=201", "offset=-1"} {
		if code, _ := e.do(t, http.MethodGet, "/sessions?"+q, alice, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
	// SQL metacharacters in the search are literal.
	if _, body := e.do(t, http.MethodGet, "/sessions?q=%25", alice, nil); body["total"].(float64) != 0 {
		t.Errorf("percent search matched %v", body["total"])
	}
}

func TestStats(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	running := e.createNamed(t, alice, "a", "codex")
	e.setStatus(t, running, []string{session.StatusPending}, session.StatusRunning)
	ok := e.createNamed(t, alice, "b", "codex")
	e.stop(t, ok)
	bad := e.createNamed(t, alice, "c", "codex")
	e.stop(t, bad)
	if _, err := e.st.Pool().Exec(context.Background(), `UPDATE sessions SET error = 'boom' WHERE id = $1`, bad); err != nil {
		t.Fatal(err)
	}
	_, body := e.do(t, http.MethodGet, "/stats", alice, nil)
	want := map[string]float64{"total": 3, "running": 1, "active": 1, "failed": 1, "failure_rate": 0.5}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s: got %v want %v", k, body[k], v)
		}
	}
	if _, body := e.do(t, http.MethodGet, "/stats", bob, nil); body["total"] != float64(0) {
		t.Errorf("stats leak across owners: %v", body)
	}
}
