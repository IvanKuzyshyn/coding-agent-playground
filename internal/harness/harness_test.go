package harness

import (
	"slices"
	"strings"
	"testing"

	"terrarium/internal/session"
)

// Lines captured from the spikes (Claude Code 2.1.296, Codex 0.162.1), trimmed.
var (
	claudeInit    = `{"type":"system","subtype":"init","cwd":"/home/agent","session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab","tools":["Bash"]}`
	claudeMessage = `{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]},"session_id":"be35dd33"}`
	claudeResult  = `{"type":"result","subtype":"success","is_error":false,"result":"ok","num_turns":3,"session_id":"be35dd33-1b7b-415f-865d-7ad0fe2e22ab","usage":{"input_tokens":4,"output_tokens":272,"cache_read_input_tokens":100,"cache_creation_input_tokens":10}}`
	claudeMaxTurn = `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":50,"session_id":"x","usage":{}}`
	codexThread   = `{"type":"thread.started","thread_id":"01a12548-5ca3-7e01-b3c4-f15ef9754926"}`
	codexMessage  = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"41"}}`
	codexDone     = `{"type":"turn.completed","usage":{"input_tokens":22526,"cached_input_tokens":9600,"cache_write_input_tokens":0,"output_tokens":10,"reasoning_output_tokens":0}}`
	codexFailed   = `{"type":"turn.failed","error":{"message":"Fatal error: boom"}}`
)

func TestParseClaude(t *testing.T) {
	u, _, ok := ParseLine(session.HarnessClaude, []byte(claudeInit))
	if !ok || u.HarnessSession != "be35dd33-1b7b-415f-865d-7ad0fe2e22ab" {
		t.Fatalf("init: %+v", u)
	}
	if u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeMessage)); u.Message != "ok" {
		t.Fatalf("message: %+v", u)
	}
	u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeResult))
	if !u.Final || u.Failed || u.Message != "ok" || u.Usage.InputTokens != 14 || u.Usage.OutputTokens != 272 || u.Usage.AgentSteps != 3 {
		t.Fatalf("result: %+v %+v", u, u.Usage)
	}
	if u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeMaxTurn)); !u.Final || !u.Failed || !strings.HasPrefix(u.Error, "error_max_turns") {
		t.Fatalf("max turns: %+v", u)
	}
	if _, _, ok := ParseLine(session.HarnessClaude, []byte("not json")); ok {
		t.Fatal("non-JSON accepted")
	}
}

func TestParseCodex(t *testing.T) {
	if u, _, _ := ParseLine(session.HarnessCodex, []byte(codexThread)); u.HarnessSession != "01a12548-5ca3-7e01-b3c4-f15ef9754926" {
		t.Fatalf("thread: %+v", u)
	}
	if u, _, _ := ParseLine(session.HarnessCodex, []byte(codexMessage)); u.Message != "41" {
		t.Fatalf("message: %+v", u)
	}
	u, _, _ := ParseLine(session.HarnessCodex, []byte(codexDone))
	if !u.Final || u.Failed || u.Usage.ThreadInputTokens != 12926 || u.Usage.ThreadCachedInputTokens != 9600 {
		t.Fatalf("done: %+v %+v", u, u.Usage)
	}
	// Spike S2 totals: turn 1 = 11250 input (9600 cached), 5 output; turn 2 thread totals above.
	prev := Usage{ThreadInputTokens: 1650, ThreadOutputTokens: 5, ThreadCachedInputTokens: 9600}
	if got := u.Usage.PerTurn(&prev); got.InputTokens != 11276 || got.OutputTokens != 5 || got.CachedInputTokens != 0 {
		t.Fatalf("per turn: %+v", got)
	}
	if got := u.Usage.PerTurn(nil); got.InputTokens != 12926 {
		t.Fatalf("first turn: %+v", got)
	}
	if u, _, _ := ParseLine(session.HarnessCodex, []byte(codexFailed)); !u.Final || !u.Failed || u.Error != "Fatal error: boom" {
		t.Fatalf("failed: %+v", u)
	}
}

func TestTurnArgv(t *testing.T) {
	spec := session.Spec{Harness: session.HarnessClaude, Limits: session.Limits{MaxAgentSteps: 5}}
	argv := TurnArgv(spec, "-rf /", "sid")
	if !slices.Contains(argv, "--resume") || argv[len(argv)-2] != "--" || argv[len(argv)-1] != "-rf /" || !slices.Contains(argv, "5") {
		t.Errorf("claude argv: %v", argv)
	}
	spec.Harness = session.HarnessCodex
	argv = TurnArgv(spec, "-rf /", "tid")
	want := []string{"codex", "exec", "resume", "tid", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--", "-rf /"}
	if !slices.Equal(argv, want) {
		t.Errorf("codex argv:\n got %v\nwant %v", argv, want)
	}
}

func TestContainerEnvHasNoRealSecrets(t *testing.T) {
	env := ContainerEnv(session.Spec{Harness: session.HarnessClaude}, Gateway{URL: "http://gateway:8080", ProxyHost: "gateway:3128"}, strings.Repeat("a", 64))
	if !strings.HasPrefix(env["CLAUDE_CODE_OAUTH_TOKEN"], "sk-ant-oat01-terrarium-") || env["NO_PROXY"] != "gateway" {
		t.Errorf("env: %v", env)
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("ANTHROPIC_API_KEY must not be set; it takes precedence over the OAuth token")
	}
}
