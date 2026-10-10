package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/gomega"

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
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	cs := e.mcp(t, alice)

	tools, err := cs.ListTools(context.Background(), nil)
	g.Expect(err).NotTo(HaveOccurred())
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"create_session", "get_session", "list_options", "list_sessions", "send_follow_up", "terminate_session", "wait_for_session"}
	g.Expect(names).To(Equal(want), "tools")

	opts, _, _ := call(t, cs, "list_options", nil)
	g.Expect(len(opts["presets"].([]any))).To(BeNumerically(">=", 2), "list_options: %v", opts)
	g.Expect(opts["harnesses"]).To(HaveLen(2), "list_options: %v", opts)

	_, isErr, text := call(t, cs, "create_session", map[string]any{"name": "   ", "harness": "codex", "prompt": "hi"})
	g.Expect(isErr).To(BeTrue(), "missing name: %q", text)
	g.Expect(text).To(ContainSubstring("name is required"), "missing name")
	created, isErr, text := call(t, cs, "create_session", map[string]any{"name": "via mcp", "harness": "codex", "prompt": "hi", "repo": "octo/hello", "keep_idle": true})
	g.Expect(isErr).To(BeFalse(), "create: %s", text)
	id := created["id"].(string)
	g.Expect(created).To(HaveKeyWithValue("status", session.StatusPending), "create")
	g.Expect(created["ui_url"]).To(HaveSuffix("/sessions/"+id), "create")
	g.Expect(created["lifecycle"]).To(HaveKeyWithValue("on_complete", "keep_idle"), "create")

	got, _, _ := call(t, cs, "get_session", map[string]any{"id": id, "include_events": true})
	turns := got["turns"].([]any)
	g.Expect(turns).To(HaveLen(1), "get: %v", got)
	g.Expect(turns[0]).To(HaveKeyWithValue("prompt", "hi"), "get")
	g.Expect(got["events"]).NotTo(BeNil(), "get: %v", got)
	fu, isErr, text := call(t, cs, "send_follow_up", map[string]any{"id": id, "prompt": "more"})
	g.Expect(isErr).To(BeFalse(), "follow-up: %s", text)
	g.Expect(fu).To(HaveKeyWithValue("seq", float64(2)), "follow-up")

	list, _, _ := call(t, cs, "list_sessions", map[string]any{"harness": []string{"codex"}, "query": "via"})
	g.Expect(list).To(HaveKeyWithValue("total", float64(1)), "list")
	_, isErr, _ = call(t, cs, "list_sessions", map[string]any{"status": []string{"bogus"}})
	g.Expect(isErr).To(BeTrue(), "unknown status accepted")

	start := time.Now()
	waited, _, _ := call(t, cs, "wait_for_session", map[string]any{"id": id, "timeout_seconds": 1})
	g.Expect(waited).To(HaveKeyWithValue("timed_out", true), "wait on a pending session")
	g.Expect(time.Since(start)).To(BeNumerically(">=", time.Second), "wait on a pending session")

	ended, isErr, text := call(t, cs, "terminate_session", map[string]any{"id": id})
	g.Expect(isErr).To(BeFalse(), "terminate: %s", text)
	g.Expect(ended).To(HaveKeyWithValue("status", session.StatusStopping), "terminate")
	_, isErr, _ = call(t, cs, "send_follow_up", map[string]any{"id": id, "prompt": "late"})
	g.Expect(isErr).To(BeTrue(), "follow-up accepted on an ending session")
}

func TestMCPWaitReturnsWhenSettled(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	e := newEnv(t, 10*time.Millisecond)
	cs := e.mcp(t, alice)
	id := e.create(t, alice)
	e.stop(t, id)
	waited, _, _ := call(t, cs, "wait_for_session", map[string]any{"id": id.String(), "timeout_seconds": 30})
	g.Expect(waited).To(HaveKeyWithValue("status", session.StatusStopped), "wait")
	g.Expect(waited).NotTo(HaveKeyWithValue("timed_out", true), "wait")
}

func TestMCPOwnershipAndAuth(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	id := e.create(t, alice)
	e.stop(t, id)
	cs := e.mcp(t, bob)
	for _, tool := range []string{"get_session", "wait_for_session"} {
		t.Run(tool, func(t *testing.T) {
			g := NewWithT(t)
			got, isErr, text := call(t, cs, tool, map[string]any{"id": id.String()})
			g.Expect(isErr).To(BeFalse(), "%s on another user's session: %q", tool, text)
			g.Expect(got).To(HaveKeyWithValue("owner", "alice"), "%s on another user's session", tool)
		})
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"terminate_session", map[string]any{"id": id.String()}},
		{"send_follow_up", map[string]any{"id": id.String(), "prompt": "x"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			g := NewWithT(t)
			_, isErr, text := call(t, cs, tc.tool, tc.args)
			g.Expect(isErr).To(BeTrue(), "%s on another user's session: %q", tc.tool, text)
			g.Expect(text).To(ContainSubstring("only alice"), "%s on another user's session", tc.tool)
		})
	}
	g := NewWithT(t)
	list, _, _ := call(t, cs, "list_sessions", nil)
	g.Expect(list).To(HaveKeyWithValue("total", float64(1)), "list should include every user's sessions")
	list, _, _ = call(t, cs, "list_sessions", map[string]any{"owner": "bob"})
	g.Expect(list).To(HaveKeyWithValue("total", float64(0)), "owner filter")

	for _, token := range []string{"", "nope"} {
		t.Run(fmt.Sprintf("token %q", token), func(t *testing.T) {
			g := NewWithT(t)
			req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			g.Expect(err).NotTo(HaveOccurred())
			resp.Body.Close()
			g.Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	}
}
