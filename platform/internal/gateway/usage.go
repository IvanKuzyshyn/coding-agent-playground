package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"terrarium/internal/pricing"
)

type usageKind int

const (
	usageNone usageKind = iota
	usageClaude
	usageCodex
)

// Bounds on what the tee keeps in memory. Oversized lines or bodies are skipped, not failed.
const (
	maxSSELine  = 1 << 20
	maxJSONBody = 8 << 20
)

// usageTee passes the upstream body through unchanged and parses usage from the bytes as they go
// by. It never holds bytes back, so streaming is unaffected. It reports once, at EOF or Close.
type usageTee struct {
	rc     io.ReadCloser
	kind   usageKind
	sse    bool
	sniff  bool // no usable Content-Type: decide SSE vs JSON from the first byte
	report func(pricing.Usage)

	line     []byte
	skipLine bool
	body     []byte
	skipBody bool

	usage pricing.Usage

	once sync.Once
}

func newUsageTee(rc io.ReadCloser, kind usageKind, contentType string, report func(pricing.Usage)) io.ReadCloser {
	ct := strings.ToLower(contentType)
	t := &usageTee{rc: rc, kind: kind, report: report}
	switch {
	case strings.Contains(ct, "text/event-stream"):
		t.sse = true
	case strings.Contains(ct, "json"):
	case ct == "":
		// chatgpt.com/backend-api/codex/responses streams SSE without a Content-Type header.
		t.sniff = true
	default:
		return rc
	}
	return t
}

func (t *usageTee) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.feed(p[:n])
	}
	if err == io.EOF {
		t.finish()
	}
	return n, err
}

func (t *usageTee) Close() error {
	t.finish()
	return t.rc.Close()
}

func (t *usageTee) feed(b []byte) {
	if t.sniff {
		trimmed := bytes.TrimLeft(b, " \t\r\n")
		if len(trimmed) == 0 {
			return
		}
		t.sniff = false
		t.sse = trimmed[0] != '{'
		b = trimmed
	}
	if !t.sse {
		if t.skipBody {
			return
		}
		if len(t.body)+len(b) > maxJSONBody {
			t.skipBody, t.body = true, nil
			return
		}
		t.body = append(t.body, b...)
		return
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if !t.skipLine {
			if len(t.line)+len(chunk) > maxSSELine {
				t.skipLine, t.line = true, t.line[:0]
			} else {
				t.line = append(t.line, chunk...)
			}
		}
		if i < 0 {
			return
		}
		if !t.skipLine {
			t.sseLine(t.line)
		}
		t.line, t.skipLine = t.line[:0], false
		b = b[i+1:]
	}
}

func (t *usageTee) sseLine(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:"))
	if !ok {
		return
	}
	t.event(bytes.TrimSpace(data))
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

func (u claudeUsage) toUsage() pricing.Usage {
	return pricing.Usage{Input: u.InputTokens, CacheWrite: u.CacheCreationInputTokens,
		CacheRead: u.CacheReadInputTokens, Output: u.OutputTokens}
}

type usageEvent struct {
	Type    string       `json:"type"`
	Usage   *claudeUsage `json:"usage"`
	Message *struct {
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	Response *struct {
		Usage *struct {
			InputTokens        int64 `json:"input_tokens"`
			InputTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// event applies one SSE data payload or a whole JSON body. Malformed data is ignored.
func (t *usageTee) event(data []byte) {
	var e usageEvent
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch t.kind {
	case usageClaude:
		switch {
		case e.Type == "message_start" && e.Message != nil && e.Message.Usage != nil:
			t.usage = e.Message.Usage.toUsage()
		case e.Type == "message_delta" && e.Usage != nil:
			// Cumulative, so the last one wins.
			t.usage.Output = e.Usage.OutputTokens
		case !t.sse && e.Usage != nil:
			t.usage = e.Usage.toUsage()
		}
	case usageCodex:
		if e.Type == "response.completed" && e.Response != nil && e.Response.Usage != nil {
			u := e.Response.Usage
			var cached int64
			if u.InputTokensDetails != nil {
				cached = u.InputTokensDetails.CachedTokens
			}
			t.usage = pricing.Usage{Input: max(u.InputTokens-cached, 0), CacheRead: cached, Output: u.OutputTokens}
		}
	}
}

func (t *usageTee) finish() {
	t.once.Do(func() {
		if !t.sse && !t.skipBody && len(t.body) > 0 {
			t.event(t.body)
		}
		t.body = nil
		if t.usage != (pricing.Usage{}) {
			t.report(t.usage)
		}
	})
}
