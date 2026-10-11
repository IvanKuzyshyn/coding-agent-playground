package gateway

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"terrarium/internal/session"
)

// The injection guard matches a few well-known injection phrasings in tool output. It catches only
// naive payloads and is not a security boundary: containment (placeholders, egress allowlist,
// budgets) is. Its value is visibility. See docs/threat-model.md §3.1.

type guardRule struct {
	name string
	re   *regexp.Regexp
}

// Rules run on normalized text (lowercase, invisible characters removed, whitespace collapsed).
// They are kept narrow because repositories routinely discuss prompts and instructions.
var guardRules = []guardRule{
	{"override", regexp.MustCompile(`\b(ignore|disregard|forget|override) (all |any )?(of )?(the |your |my )?(previous|prior|above|earlier|preceding|system) (instructions?|prompts?|directions|rules|guidelines)\b`)},
	{"prompt_leak", regexp.MustCompile(`\b(reveal|print|show|output|repeat|leak) (me )?(your|the) (system prompt|hidden instructions|initial instructions)\b`)},
	{"mode_switch", regexp.MustCompile(`\b(enable|enter|activate|switch to) (developer|god|dan|jailbreak) mode\b`)},
	{"chat_markup", regexp.MustCompile(`<\|im_(start|end)\|>|</?(tool_result|function_results)>`)},
	// Tool output forging the turn outcome marker the agent guide defines (internal/harness).
	{"outcome_marker", regexp.MustCompile(`terrarium_outcome`)},
}

type guardHit struct {
	Rule    string
	Excerpt string
}

// scanInference returns one hit per matching rule in the request's newest tool output. Only the
// output added since the previous request is scanned: each request resends the whole
// conversation, so scanning all of it would re-flag an old match on every request.
func scanInference(harness string, body []byte) []guardHit {
	text := normalize(newToolOutput(harness, body))
	var hits []guardHit
	for _, r := range guardRules {
		if loc := r.re.FindStringIndex(text); loc != nil {
			hits = append(hits, guardHit{Rule: r.name, Excerpt: excerpt(text, loc)})
		}
	}
	return hits
}

// newToolOutput extracts the tool results the harness is sending for the first time: Claude's
// tool_result blocks in the last (user) message, Codex's trailing *_call_output input items.
func newToolOutput(harness string, body []byte) string {
	var sb strings.Builder
	if harness == session.HarnessClaude {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
			return ""
		}
		last := req.Messages[len(req.Messages)-1]
		var blocks []struct {
			Type    string `json:"type"`
			Content any    `json:"content"`
		}
		if last.Role != "user" || json.Unmarshal(last.Content, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				collectText(&sb, b.Content)
			}
		}
		return sb.String()
	}
	var req struct {
		Input []struct {
			Type   string `json:"type"`
			Output any    `json:"output"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	for i := len(req.Input) - 1; i >= 0 && strings.HasSuffix(req.Input[i].Type, "_call_output"); i-- {
		collectText(&sb, req.Input[i].Output)
	}
	return sb.String()
}

// collectText appends every string in a decoded JSON value, skipping image payloads.
func collectText(sb *strings.Builder, v any) {
	switch v := v.(type) {
	case string:
		sb.WriteString(v)
		sb.WriteByte('\n')
	case []any:
		for _, e := range v {
			collectText(sb, e)
		}
	case map[string]any:
		for k, e := range v {
			if k != "data" && k != "image_url" {
				collectText(sb, e)
			}
		}
	}
}

// normalize defeats the cheapest evasions: case, zero-width and other invisible format
// characters inside words, and whitespace or line breaks between words.
func normalize(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func excerpt(text string, loc []int) string {
	start, end := max(loc[0]-40, 0), min(loc[1]+40, len(text))
	// Widen to rune boundaries so the excerpt stays valid UTF-8.
	for start > 0 && !isRuneStart(text[start]) {
		start--
	}
	for end < len(text) && !isRuneStart(text[end]) {
		end++
	}
	return truncate(text[start:end])
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
