package harness

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"

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
	g := NewWithT(t)
	u, _, ok := ParseLine(session.HarnessClaude, []byte(claudeInit))
	g.Expect(ok).To(BeTrue(), "init")
	g.Expect(u.HarnessSession).To(Equal("be35dd33-1b7b-415f-865d-7ad0fe2e22ab"), "init")
	u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeMessage))
	g.Expect(u.Message).To(Equal("ok"), "message")
	u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeResult))
	g.Expect(u.Final).To(BeTrue(), "result")
	g.Expect(u.Failed).To(BeFalse(), "result")
	g.Expect(u.Message).To(Equal("ok"), "result")
	g.Expect(u.Usage.InputTokens).To(Equal(int64(14)), "result")
	g.Expect(u.Usage.OutputTokens).To(Equal(int64(272)), "result")
	g.Expect(u.Usage.AgentSteps).To(Equal(3), "result")
	u, _, _ = ParseLine(session.HarnessClaude, []byte(claudeMaxTurn))
	g.Expect(u.Final).To(BeTrue(), "max turns")
	g.Expect(u.Failed).To(BeTrue(), "max turns")
	g.Expect(u.Error).To(HavePrefix("error_max_turns"), "max turns")
	_, _, ok = ParseLine(session.HarnessClaude, []byte("not json"))
	g.Expect(ok).To(BeFalse(), "non-JSON accepted")
}

func TestParseCodex(t *testing.T) {
	g := NewWithT(t)
	u, _, _ := ParseLine(session.HarnessCodex, []byte(codexThread))
	g.Expect(u.HarnessSession).To(Equal("01a12548-5ca3-7e01-b3c4-f15ef9754926"), "thread")
	u, _, _ = ParseLine(session.HarnessCodex, []byte(codexMessage))
	g.Expect(u.Message).To(Equal("41"), "message")
	u, _, _ = ParseLine(session.HarnessCodex, []byte(codexDone))
	g.Expect(u.Final).To(BeTrue(), "done")
	g.Expect(u.Failed).To(BeFalse(), "done")
	g.Expect(u.Usage.ThreadInputTokens).To(Equal(int64(12926)), "done")
	g.Expect(u.Usage.ThreadCachedInputTokens).To(Equal(int64(9600)), "done")
	// Spike S2 totals: turn 1 = 11250 input (9600 cached), 5 output; turn 2 thread totals above.
	prev := Usage{ThreadInputTokens: 1650, ThreadOutputTokens: 5, ThreadCachedInputTokens: 9600}
	got := u.Usage.PerTurn(&prev)
	g.Expect(got.InputTokens).To(Equal(int64(11276)), "per turn")
	g.Expect(got.OutputTokens).To(Equal(int64(5)), "per turn")
	g.Expect(got.CachedInputTokens).To(Equal(int64(0)), "per turn")
	g.Expect(u.Usage.PerTurn(nil).InputTokens).To(Equal(int64(12926)), "first turn")
	u, _, _ = ParseLine(session.HarnessCodex, []byte(codexFailed))
	g.Expect(u.Final).To(BeTrue(), "failed")
	g.Expect(u.Failed).To(BeTrue(), "failed")
	g.Expect(u.Error).To(Equal("Fatal error: boom"), "failed")
}

func TestTurnArgv(t *testing.T) {
	g := NewWithT(t)
	spec := session.Spec{Harness: session.HarnessClaude, Limits: session.Limits{MaxAgentSteps: 5}}
	argv := TurnArgv(spec, "-rf /", "sid")
	g.Expect(argv).To(ContainElements("--resume", "5"), "claude argv")
	g.Expect(argv[len(argv)-2:]).To(Equal([]string{"--", "-rf /"}), "claude argv")
	spec.Harness = session.HarnessCodex
	argv = TurnArgv(spec, "-rf /", "tid")
	want := []string{"codex", "exec", "resume", "tid", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "--", "-rf /"}
	g.Expect(argv).To(Equal(want), "codex argv")
}

func TestContainerEnvHasNoRealSecrets(t *testing.T) {
	g := NewWithT(t)
	env := ContainerEnv("sid", session.Spec{Harness: session.HarnessClaude}, Gateway{URL: "http://gateway:8080", ProxyHost: "gateway:3128"}, strings.Repeat("a", 64))
	g.Expect(env["CLAUDE_CODE_OAUTH_TOKEN"]).To(HavePrefix("sk-ant-oat01-terrarium-"))
	g.Expect(env).To(HaveKeyWithValue("NO_PROXY", "gateway"))
	g.Expect(env).NotTo(HaveKey("ANTHROPIC_API_KEY"), "ANTHROPIC_API_KEY must not be set; it takes precedence over the OAuth token")
}

func TestContainerEnvRoutesGitHubThroughGateway(t *testing.T) {
	g := NewWithT(t)
	secret := strings.Repeat("a", 64)
	gw := Gateway{URL: "http://gateway:8080", ProxyHost: "gateway:3128", GitName: "Ada", GitEmail: "ada@example.com"}
	env := ContainerEnv("sid", session.Spec{Harness: session.HarnessCodex, Repo: "octo/hello"}, gw, secret)
	github := "http://terrarium:" + secret + "@gateway:8080/github"
	g.Expect(env).To(HaveKeyWithValue("GIT_CONFIG_KEY_0", "url."+github+"/.insteadOf"), "git rewrite")
	g.Expect(env).To(HaveKeyWithValue("GIT_CONFIG_VALUE_0", "https://github.com/"), "git rewrite")
	g.Expect(env).To(HaveKeyWithValue("TERRARIUM_GITHUB_URL", github))
	g.Expect(env).To(HaveKeyWithValue("TERRARIUM_REPO", "octo/hello"))
	g.Expect(env).To(HaveKeyWithValue("TERRARIUM_BRANCH_PREFIX", "terrarium/sid/"))
	g.Expect(env).To(HaveKeyWithValue("GIT_AUTHOR_EMAIL", "ada@example.com"), "identity")
	g.Expect(env).To(HaveKeyWithValue("GIT_COMMITTER_NAME", "Ada"), "identity")
}

func TestSetupFilesPublishGuide(t *testing.T) {
	gw := Gateway{URL: "http://gateway:8080"}
	for harness, path := range map[string]string{session.HarnessClaude: Home + "/.claude/CLAUDE.md", session.HarnessCodex: Home + "/.codex/AGENTS.md"} {
		t.Run(harness, func(t *testing.T) {
			g := NewWithT(t)
			files := SetupFiles("sid", session.Spec{Harness: harness, Repo: "octo/hello"}, gw, strings.Repeat("a", 64), "")
			g.Expect(files).NotTo(BeEmpty())
			g.Expect(files[0].Path).To(Equal(path))
			g.Expect(string(files[0].Data)).To(ContainSubstring("terrarium/sid/"))
			files = SetupFiles("sid", session.Spec{Harness: harness}, gw, strings.Repeat("a", 64), "")
			guide := string(files[0].Data)
			g.Expect(files[0].Path).To(Equal(path), "without a repo")
			g.Expect(guide).To(ContainSubstring(OutcomeMarker), "without a repo")
			g.Expect(guide).NotTo(ContainSubstring("/pulls"), "without a repo")
		})
	}
}

func TestReportedFailure(t *testing.T) {
	cases := []struct {
		reply, reason, rest string
		failed              bool
	}{
		{"Done.", "", "Done.", false},
		{"I could not reach it.\n\nTERRARIUM_OUTCOME: failed - wttr.in is not reachable\n", "wttr.in is not reachable", "I could not reach it.", true},
		{"TERRARIUM_OUTCOME: failed: denied", "denied", "", true},
		{"Sorry.\nTERRARIUM_OUTCOME: failed", "no reason given", "Sorry.", true},
		// Only the last line counts: quoting the marker mid-reply is not a failure.
		{"Use TERRARIUM_OUTCOME: failed when stuck.\nAll good.", "", "Use TERRARIUM_OUTCOME: failed when stuck.\nAll good.", false},
	}
	for _, c := range cases {
		t.Run(c.reply, func(t *testing.T) {
			g := NewWithT(t)
			reason, rest, failed := ReportedFailure(c.reply)
			g.Expect(reason).To(Equal(c.reason))
			g.Expect(rest).To(Equal(c.rest))
			g.Expect(failed).To(Equal(c.failed))
		})
	}
}
