package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"terrarium/internal/credentials"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// Production GitHub upstreams.
const (
	DefaultGitHubBaseURL    = "https://github.com"
	DefaultGitHubAPIBaseURL = "https://api.github.com"
)

const (
	maxPRBody       = 1 << 20
	maxPushCommands = 1 << 20
)

type githubKind int

const (
	githubFetch githubKind = iota
	githubPushRefs
	githubPush
	githubPR
)

type githubReq struct {
	kind     githubKind
	upstream *url.URL
}

type githubCtxKey struct{}

// serveGitHub handles /github/: git smart HTTP for the session's repository and PR creation on
// it. The sandbox authenticates with Basic terrarium:<secret>; the gateway's GitHub token is
// attached only after the repository, branch and session checks pass.
func (g *Gateway) serveGitHub(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	secret, ok := proxySecret(r.Header.Get("Authorization"))
	if !ok {
		// git sends credentials only after a 401 challenge.
		w.Header().Set("WWW-Authenticate", `Basic realm="terrarium"`)
		writeError(w, http.StatusUnauthorized, "credential required")
		return
	}
	s, err := g.Store.GatewaySessionBySecretHash(ctx, credentials.Hash(secret))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, "missing or unknown credential")
		return
	}
	if err != nil {
		slog.Error("session lookup", "err", err)
		writeError(w, http.StatusServiceUnavailable, "session lookup failed")
		return
	}
	// Provisioning is included: the repository is cloned through this route before the first turn.
	if !session.EgressAllowed(s.Status) {
		g.denyGitHub(ctx, s, r, "status")
		writeError(w, http.StatusForbidden, "session not live")
		return
	}
	gr, ok := g.matchGitHub(r, s.Repo)
	if !ok {
		g.denyGitHub(ctx, s, r, "route")
		writeError(w, http.StatusForbidden, "route not allowed")
		return
	}
	_, hasToken := g.github.get()
	if gr.kind != githubFetch && !hasToken {
		writeError(w, http.StatusServiceUnavailable, "github credential unavailable")
		return
	}

	prefix := session.BranchPrefix(s.ID.String())
	switch gr.kind {
	case githubPush:
		if r.Header.Get("Content-Encoding") != "" {
			g.denyGitHub(ctx, s, r, "encoding")
			writeError(w, http.StatusUnsupportedMediaType, "encoded push bodies are not supported")
			return
		}
		refs, head, err := readPushCommands(r.Body)
		if err != nil {
			g.denyGitHub(ctx, s, r, "push: "+err.Error())
			writeError(w, http.StatusBadRequest, "malformed push")
			return
		}
		for _, c := range refs {
			if msg := pushAllowed(c, prefix); msg != "" {
				g.denyGitHub(ctx, s, r, "push to "+truncate(c.ref)+": "+msg)
				writeError(w, http.StatusForbidden, msg)
				return
			}
		}
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), r.Body))
		r.ContentLength = -1
		g.allowGitHub(ctx, s, r, map[string]any{"refs": refNames(refs)})
	case githubPR:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxPRBody+1))
		if err != nil || len(body) > maxPRBody {
			writeError(w, http.StatusBadRequest, "read request body")
			return
		}
		var pr struct {
			Head string `json:"head"`
		}
		// "owner:branch" names a branch in another repository (a fork); only this one is allowed.
		if json.Unmarshal(body, &pr) != nil || pr.Head == "" || strings.Contains(pr.Head, ":") {
			g.denyGitHub(ctx, s, r, "pr head "+truncate(pr.Head))
			writeError(w, http.StatusForbidden, "pull request head must be a branch of this repository")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		g.allowGitHub(ctx, s, r, map[string]any{"head": pr.Head})
	}
	slog.Info("github forward", "session", s.ID, "method", r.Method, "path", r.URL.Path)
	g.githubProxy.ServeHTTP(w, r.WithContext(context.WithValue(ctx, githubCtxKey{}, gr)))
}

// matchGitHub allows git fetch and push of exactly the session's repository and PR creation on
// it. GitHub names are case-insensitive.
func (g *Gateway) matchGitHub(r *http.Request, repo string) (githubReq, bool) {
	if repo == "" || r.URL.RawPath != "" {
		return githubReq{}, false
	}
	p := strings.TrimPrefix(r.URL.Path, "/github/")
	if rest, ok := strings.CutPrefix(p, "api/repos/"); ok {
		if r.Method == http.MethodPost && strings.EqualFold(rest, repo+"/pulls") && r.URL.RawQuery == "" {
			return githubReq{githubPR, g.githubURL(g.githubAPIBase, "/repos/"+repo+"/pulls", "")}, true
		}
		return githubReq{}, false
	}
	var name, op string
	for _, suffix := range []string{"/info/refs", "/git-upload-pack", "/git-receive-pack"} {
		if n, ok := strings.CutSuffix(p, suffix); ok {
			name, op = strings.TrimSuffix(n, ".git"), suffix
		}
	}
	if !strings.EqualFold(name, repo) {
		return githubReq{}, false
	}
	upstreamPath := "/" + repo + ".git" + op
	switch {
	case op == "/info/refs" && r.Method == http.MethodGet:
		q := r.URL.Query()
		svc := q.Get("service")
		if len(q) != 1 || (svc != "git-upload-pack" && svc != "git-receive-pack") {
			return githubReq{}, false
		}
		kind := githubFetch
		if svc == "git-receive-pack" {
			kind = githubPushRefs
		}
		return githubReq{kind, g.githubURL(g.githubBase, upstreamPath, "service="+svc)}, true
	case op == "/git-upload-pack" && r.Method == http.MethodPost && r.URL.RawQuery == "":
		return githubReq{githubFetch, g.githubURL(g.githubBase, upstreamPath, "")}, true
	case op == "/git-receive-pack" && r.Method == http.MethodPost && r.URL.RawQuery == "":
		return githubReq{githubPush, g.githubURL(g.githubBase, upstreamPath, "")}, true
	}
	return githubReq{}, false
}

func (g *Gateway) githubURL(base *url.URL, path, query string) *url.URL {
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + path
	u.RawQuery = query
	return &u
}

type pushCommand struct{ oldOID, newOID, ref string }

// zero reports git's all-zero object id (SHA-1 or SHA-256 length), which marks a ref that does
// not exist on one side of the update.
func zero(oid string) bool { return oid != "" && strings.Trim(oid, "0") == "" }

// pushAllowed returns why a ref update is refused, or "". Branches that already exist may be
// updated; new ones must use the session's prefix; deletes and non-branch refs are refused.
// GitHub applies an update only if oldOID matches the branch, so a non-zero oldOID cannot create
// a branch.
func pushAllowed(c pushCommand, prefix string) string {
	branch, ok := strings.CutPrefix(c.ref, "refs/heads/")
	switch {
	case !ok:
		return "only branches can be pushed"
	case zero(c.newOID):
		return "deleting branches is not allowed"
	case zero(c.oldOID) && !strings.HasPrefix(branch, prefix):
		return "new branches must be named " + prefix + "<name>"
	}
	return ""
}

// readPushCommands reads the receive-pack command list (pkt-lines up to the first flush) and
// returns it with the raw bytes consumed, so they can be replayed upstream before the pack.
// Shallow lines from shallow clones precede the commands.
func readPushCommands(body io.Reader) ([]pushCommand, []byte, error) {
	var raw bytes.Buffer
	br := bufio.NewReader(io.LimitReader(body, maxPushCommands))
	var cmds []pushCommand
	for {
		var n [4]byte
		if _, err := io.ReadFull(br, n[:]); err != nil {
			return nil, nil, fmt.Errorf("read pkt-line: %w", err)
		}
		raw.Write(n[:])
		size, err := strconv.ParseUint(string(n[:]), 16, 16)
		if err != nil {
			return nil, nil, errors.New("bad pkt-line length")
		}
		if size == 0 {
			break
		}
		if size < 4 {
			return nil, nil, errors.New("bad pkt-line length")
		}
		line := make([]byte, size-4)
		if _, err := io.ReadFull(br, line); err != nil {
			return nil, nil, fmt.Errorf("read pkt-line: %w", err)
		}
		raw.Write(line)
		text, _, _ := strings.Cut(strings.TrimSuffix(string(line), "\n"), "\x00")
		if strings.HasPrefix(text, "shallow ") {
			continue
		}
		f := strings.Fields(text)
		if len(f) != 3 {
			return nil, nil, errors.New("bad command")
		}
		cmds = append(cmds, pushCommand{f[0], f[1], f[2]})
	}
	if len(cmds) == 0 {
		return nil, nil, errors.New("no commands")
	}
	// Bytes already buffered past the flush belong to the pack and are replayed too.
	rest, _ := br.Peek(br.Buffered())
	raw.Write(rest)
	return cmds, raw.Bytes(), nil
}

func refNames(cmds []pushCommand) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.ref
	}
	return names
}

func (g *Gateway) allowGitHub(ctx context.Context, s store.GatewaySession, r *http.Request, extra map[string]any) {
	p := map[string]any{"decision": "allowed", "via": "github", "method": r.Method, "path": truncate(r.URL.Path)}
	for k, v := range extra {
		p[k] = v
	}
	g.event(ctx, s.ID, p)
}

func (g *Gateway) denyGitHub(ctx context.Context, s store.GatewaySession, r *http.Request, reason string) {
	slog.Info("github denied", "session", s.ID, "method", r.Method, "path", truncate(r.URL.Path), "reason", reason)
	g.event(ctx, s.ID, map[string]any{
		"decision": "denied", "via": "github", "method": r.Method, "path": truncate(r.URL.Path), "reason": reason,
	})
}

func (g *Gateway) newGitHubProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			gr := pr.In.Context().Value(githubCtxKey{}).(githubReq)
			pr.Out.URL = gr.upstream
			pr.Out.Host = ""
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
		},
		Transport:     githubTransport{g},
		FlushInterval: -1,
		ErrorHandler:  g.proxyError,
	}
}

// githubTransport attaches the token at the last moment, like credentialTransport. Fetches go
// without a token when none is configured, so public repositories still clone.
type githubTransport struct{ g *Gateway }

func (t githubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	gr := req.Context().Value(githubCtxKey{}).(githubReq)
	if token, ok := t.g.github.get(); ok {
		if gr.kind == githubPR {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
		}
	} else if gr.kind != githubFetch {
		return nil, errNoCredential
	}
	return t.g.Transport.RoundTrip(req)
}
