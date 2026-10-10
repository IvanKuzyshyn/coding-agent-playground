// Package harness knows how to run one turn of Claude Code or Codex inside a sandbox and how to
// read their JSON event streams. Each turn is one process; later turns resume the harness's own
// session (plan D6).
package harness

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"terrarium/internal/credentials"
	"terrarium/internal/session"
)

const (
	Home    = "/home/agent"
	WorkDir = Home + "/work"
)

// Gateway is how a sandbox reaches the gateway.
type Gateway struct {
	URL       string // e.g. http://gateway:8080
	ProxyHost string // e.g. gateway:3128
	// Commit identity for work pushed through the gateway's GitHub route; empty leaves git's default.
	GitName  string
	GitEmail string
}

// GitHubURL is the sandbox's base URL for the gateway's /github/ route, carrying the placeholder
// credential git and terrarium-pr authenticate with.
func GitHubURL(gw Gateway, secret string) string {
	u, err := url.Parse(gw.URL)
	if err != nil {
		return ""
	}
	u.User = url.UserPassword(credentials.ProxyUser, secret)
	u.Path = strings.TrimSuffix(u.Path, "/") + "/github"
	return u.String()
}

// ContainerEnv is set once at sandbox creation. It holds placeholders only.
func ContainerEnv(sessionID string, spec session.Spec, gw Gateway, secret string) map[string]string {
	proxy := credentials.ProxyURL(gw.ProxyHost, secret)
	proxyHost, _, _ := strings.Cut(gw.ProxyHost, ":")
	github := GitHubURL(gw, secret)
	env := map[string]string{
		"HTTPS_PROXY": proxy, "https_proxy": proxy,
		"HTTP_PROXY": proxy, "http_proxy": proxy,
		"NO_PROXY": proxyHost, "no_proxy": proxyHost,
		"HOME": Home,
		// Every github.com remote goes through the gateway, which holds the GitHub token.
		"GIT_CONFIG_COUNT":        "1",
		"GIT_CONFIG_KEY_0":        "url." + github + "/.insteadOf",
		"GIT_CONFIG_VALUE_0":      "https://github.com/",
		"TERRARIUM_GITHUB_URL":    github,
		"TERRARIUM_REPO":          spec.Repo,
		"TERRARIUM_BRANCH_PREFIX": session.BranchPrefix(sessionID),
	}
	if gw.GitName != "" && gw.GitEmail != "" {
		env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"] = gw.GitName, gw.GitName
		env["GIT_AUTHOR_EMAIL"], env["GIT_COMMITTER_EMAIL"] = gw.GitEmail, gw.GitEmail
	}
	switch spec.Harness {
	case session.HarnessClaude:
		env["CLAUDE_CODE_OAUTH_TOKEN"] = credentials.ClaudeToken(secret)
		env["ANTHROPIC_BASE_URL"] = gw.URL + "/anthropic"
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	case session.HarnessCodex:
		env["CODEX_REFRESH_TOKEN_URL_OVERRIDE"] = gw.URL + "/oauth/token"
		env["CODEX_REVOKE_TOKEN_URL_OVERRIDE"] = gw.URL + "/oauth/revoke"
	}
	return env
}

// File is written into the sandbox before the first turn.
type File struct {
	Path string
	Data []byte
	Mode uint32
}

// OutcomeMarker starts the line an agent ends its final reply with when it could not complete
// the task. A harness exits normally either way, so this is the only failure signal for it.
const OutcomeMarker = "TERRARIUM_OUTCOME: failed"

const outcomeGuide = `# Reporting the outcome

If you could not complete the task (for example a host is unreachable, access is denied, or
required information is unavailable), make the last line of your final reply exactly:

` + OutcomeMarker + ` - <one-line reason>

Do not add this line when the task is done.
`

// ReportedFailure finds the outcome marker on the last non-empty line of a final reply. It
// returns the reason and the reply without the marker line.
func ReportedFailure(reply string) (reason, rest string, failed bool) {
	trimmed := strings.TrimRight(reply, " \t\r\n")
	i := strings.LastIndex(trimmed, "\n")
	last := strings.TrimSpace(trimmed[i+1:])
	after, ok := strings.CutPrefix(last, OutcomeMarker)
	if !ok {
		return "", reply, false
	}
	reason = strings.TrimSpace(strings.TrimLeft(after, " -–—:"))
	if reason == "" {
		reason = "no reason given"
	}
	if i < 0 {
		return reason, "", true
	}
	return reason, strings.TrimRight(trimmed[:i], " \t\r\n"), true
}

// agentGuide is the harness's user-level memory file (CLAUDE.md / AGENTS.md): how to report a
// failed task and, for sessions with a repository, how to publish work within what the gateway
// allows.
func agentGuide(spec session.Spec, sessionID string) string {
	if spec.Repo == "" {
		return outcomeGuide
	}
	return outcomeGuide + fmt.Sprintf(`
# Publishing changes

Only when the user asks you to push or open a pull request:

1. Commit on the branch you were asked to use if it already exists on origin; otherwise create
   one named %s<short-slug> (new branches with other names are rejected). Branches cannot be
   deleted.
2. git push -u origin <branch>
3. terrarium-pr "<title>" "<body>" opens the pull request on %s from the current branch.

No other GitHub repository or API is reachable. The gh CLI is installed but has no GitHub access
here; use git and terrarium-pr instead.
`, session.BranchPrefix(sessionID), spec.Repo)
}

// SetupFiles returns harness configuration files. lastRefresh is an RFC3339 timestamp.
func SetupFiles(sessionID string, spec session.Spec, gw Gateway, secret, lastRefresh string) []File {
	path := Home + "/.claude/CLAUDE.md"
	if spec.Harness == session.HarnessCodex {
		path = Home + "/.codex/AGENTS.md"
	}
	files := []File{{Path: path, Data: []byte(agentGuide(spec, sessionID)), Mode: 0o644}}
	if spec.Harness != session.HarnessCodex {
		return files
	}
	// Custom provider instead of the built-in "openai": plain HTTP SSE and no
	// /wham/accounts/check preflight (spikes/s2-codex-swap).
	cfg := fmt.Sprintf(`model_provider = "terrarium"
chatgpt_base_url = %q
cli_auth_credentials_store = "file"

[model_providers.terrarium]
name = "terrarium"
base_url = %q
wire_api = "responses"
requires_openai_auth = true

[analytics]
enabled = false

[otel]
metrics_exporter = "none"
`, gw.URL+"/chatgpt/", gw.URL+"/openai")
	return append(files,
		File{Path: Home + "/.codex/config.toml", Data: []byte(cfg), Mode: 0o600},
		File{Path: Home + "/.codex/auth.json", Data: credentials.CodexAuthJSON(secret, lastRefresh), Mode: 0o600},
	)
}

// TurnArgv builds the command for one turn. harnessSession is empty for the first turn.
func TurnArgv(spec session.Spec, prompt, harnessSession string) []string {
	switch spec.Harness {
	case session.HarnessClaude:
		argv := []string{"claude", "-p",
			"--output-format", "stream-json", "--verbose",
			"--permission-mode", "bypassPermissions"}
		if spec.Limits.MaxAgentSteps > 0 {
			argv = append(argv, "--max-turns", strconv.Itoa(spec.Limits.MaxAgentSteps))
		}
		if spec.Model != "" {
			argv = append(argv, "--model", spec.Model)
		}
		if spec.Instructions != "" {
			argv = append(argv, "--append-system-prompt", spec.Instructions)
		}
		if harnessSession != "" {
			argv = append(argv, "--resume", harnessSession)
		}
		// "--" stops option parsing so a prompt starting with "-" stays a prompt.
		return append(argv, "--", prompt)
	case session.HarnessCodex:
		argv := []string{"codex", "exec"}
		if harnessSession != "" {
			argv = append(argv, "resume", harnessSession)
		}
		argv = append(argv, "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check")
		if spec.Model != "" {
			argv = append(argv, "-m", spec.Model)
		}
		// "--" stops option parsing so a prompt starting with "-" stays a prompt.
		return append(argv, "--", prompt)
	}
	return nil
}

// Usage is normalised across harnesses.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	AgentSteps        int   `json:"agent_steps"`
	// Codex reports totals for the whole thread on every turn.completed; these keep the totals so
	// the next turn can be stored as a difference. Zero for Claude, whose usage is per process.
	ThreadInputTokens       int64 `json:"thread_input_tokens,omitempty"`
	ThreadOutputTokens      int64 `json:"thread_output_tokens,omitempty"`
	ThreadCachedInputTokens int64 `json:"thread_cached_input_tokens,omitempty"`
}

// PerTurn converts thread totals into this turn's usage given the previous turn's usage.
func (u Usage) PerTurn(prev *Usage) Usage {
	if u.ThreadInputTokens == 0 && u.ThreadOutputTokens == 0 {
		return u
	}
	var p Usage
	if prev != nil {
		p = *prev
	}
	u.InputTokens = max(u.ThreadInputTokens-p.ThreadInputTokens, 0)
	u.OutputTokens = max(u.ThreadOutputTokens-p.ThreadOutputTokens, 0)
	u.CachedInputTokens = max(u.ThreadCachedInputTokens-p.ThreadCachedInputTokens, 0)
	return u
}

// Update is what one event line tells the supervisor.
type Update struct {
	HarnessSession string // set when the harness announces its session/thread id
	Message        string // latest assistant message text, if any
	Final          bool   // the turn has a terminal outcome
	Failed         bool
	Error          string
	Usage          *Usage
}

// ParseLine reads one JSON line. ok is false for lines that are not JSON objects.
func ParseLine(harnessName string, line []byte) (u Update, raw json.RawMessage, ok bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(line, &probe) != nil {
		return u, nil, false
	}
	raw = json.RawMessage(line)
	switch harnessName {
	case session.HarnessClaude:
		u = parseClaude(line)
	case session.HarnessCodex:
		u = parseCodex(line)
	}
	return u, raw, true
}

func parseClaude(line []byte) Update {
	var e struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
		NumTurns  int    `json:"num_turns"`
		Usage     struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(line, &e)
	var u Update
	switch e.Type {
	case "system":
		if e.Subtype == "init" {
			u.HarnessSession = e.SessionID
		}
	case "assistant":
		var parts []string
		for _, c := range e.Message.Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		u.Message = strings.Join(parts, "\n")
	case "result":
		u.Final = true
		u.HarnessSession = e.SessionID
		u.Usage = &Usage{
			InputTokens:       e.Usage.InputTokens + e.Usage.CacheCreationTokens,
			OutputTokens:      e.Usage.OutputTokens,
			CachedInputTokens: e.Usage.CacheReadTokens,
			AgentSteps:        e.NumTurns,
		}
		if e.IsError || e.Subtype != "success" {
			u.Failed = true
			u.Error = e.Subtype
			if e.Result != "" {
				u.Error += ": " + e.Result
			}
		} else {
			u.Message = e.Result
		}
	}
	return u
}

func parseCodex(line []byte) Update {
	var e struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Item     struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Usage struct {
			InputTokens       int64 `json:"input_tokens"`
			CachedInputTokens int64 `json:"cached_input_tokens"`
			OutputTokens      int64 `json:"output_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(line, &e)
	var u Update
	switch e.Type {
	case "thread.started":
		u.HarnessSession = e.ThreadID
	case "item.completed":
		if e.Item.Type == "agent_message" {
			u.Message = e.Item.Text
		}
	case "turn.completed":
		u.Final = true
		u.Usage = &Usage{
			ThreadInputTokens:       e.Usage.InputTokens - e.Usage.CachedInputTokens,
			ThreadOutputTokens:      e.Usage.OutputTokens,
			ThreadCachedInputTokens: e.Usage.CachedInputTokens,
		}
	case "turn.failed":
		u.Final = true
		u.Failed = true
		u.Error = e.Error.Message
	}
	return u
}
