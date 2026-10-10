package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"terrarium/internal/session"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (e env) mcp(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   e.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call runs a tool and decodes its structured output into a map; isError reports a tool error.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (out map[string]any, isError bool, text string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return out, res.IsError, text
}

func TestMCPTools(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	cs := e.mcp(t, alice)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"create_session", "get_session", "list_options", "list_sessions", "send_follow_up", "terminate_session", "wait_for_session"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v", names)
	}

	opts, _, _ := call(t, cs, "list_options", nil)
	if len(opts["presets"].([]any)) < 2 || len(opts["harnesses"].([]any)) != 2 {
		t.Errorf("list_options: %v", opts)
	}

	if _, isErr, text := call(t, cs, "create_session", map[string]any{"name": "   ", "harness": "codex", "prompt": "hi"}); !isErr || !strings.Contains(text, "name is required") {
		t.Errorf("missing name: isError=%v %q", isErr, text)
	}
	created, isErr, text := call(t, cs, "create_session", map[string]any{"name": "via mcp", "harness": "codex", "prompt": "hi", "repo": "octo/hello", "keep_idle": true})
	if isErr {
		t.Fatalf("create: %s", text)
	}
	id := created["id"].(string)
	if created["status"] != session.StatusPending || !strings.HasSuffix(created["ui_url"].(string), "/sessions/"+id) ||
		created["lifecycle"].(map[string]any)["on_complete"] != "keep_idle" {
		t.Errorf("create: %v", created)
	}

	got, _, _ := call(t, cs, "get_session", map[string]any{"id": id, "include_events": true})
	turns := got["turns"].([]any)
	if len(turns) != 1 || turns[0].(map[string]any)["prompt"] != "hi" || got["events"] == nil {
		t.Errorf("get: %v", got)
	}
	if fu, isErr, text := call(t, cs, "send_follow_up", map[string]any{"id": id, "prompt": "more"}); isErr || fu["seq"] != float64(2) {
		t.Errorf("follow-up: %v %s", fu, text)
	}

	list, _, _ := call(t, cs, "list_sessions", map[string]any{"harness": []string{"codex"}, "query": "via"})
	if list["total"] != float64(1) {
		t.Errorf("list: %v", list)
	}
	if _, isErr, _ := call(t, cs, "list_sessions", map[string]any{"status": []string{"bogus"}}); !isErr {
		t.Error("unknown status accepted")
	}

	start := time.Now()
	waited, _, _ := call(t, cs, "wait_for_session", map[string]any{"id": id, "timeout_seconds": 1})
	if waited["timed_out"] != true || time.Since(start) < time.Second {
		t.Errorf("wait on a pending session: %v after %v", waited, time.Since(start))
	}

	ended, isErr, text := call(t, cs, "terminate_session", map[string]any{"id": id})
	if isErr || ended["status"] != session.StatusStopping {
		t.Errorf("terminate: %v %s", ended, text)
	}
	if _, isErr, _ := call(t, cs, "send_follow_up", map[string]any{"id": id, "prompt": "late"}); !isErr {
		t.Error("follow-up accepted on an ending session")
	}
}

func TestMCPWaitReturnsWhenSettled(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	cs := e.mcp(t, alice)
	id := e.create(t, alice)
	e.stop(t, id)
	waited, _, _ := call(t, cs, "wait_for_session", map[string]any{"id": id.String(), "timeout_seconds": 30})
	if waited["status"] != session.StatusStopped || waited["timed_out"] == true {
		t.Errorf("wait: %v", waited)
	}
}

func TestMCPOwnershipAndAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	cs := e.mcp(t, bob)
	for _, tool := range []string{"get_session", "terminate_session", "wait_for_session"} {
		if _, isErr, text := call(t, cs, tool, map[string]any{"id": id.String()}); !isErr || !strings.Contains(text, "session not found") {
			t.Errorf("%s on another engineer's session: isError=%v %q", tool, isErr, text)
		}
	}
	if list, _, _ := call(t, cs, "list_sessions", nil); list["total"] != float64(0) {
		t.Errorf("list leaks across owners: %v", list)
	}

	for _, token := range []string{"", "nope"} {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: %d", token, resp.StatusCode)
		}
	}
}
