package gateway

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"terrarium/internal/session"
)

const realGitHubToken = "gho_real-github-token-SECRET"

type githubCall struct {
	method, path, query, auth, body string
}

type githubEnv struct {
	*env
	mu    sync.Mutex
	calls []githubCall
}

// newGitHubEnv wires a gateway to a fake github.com/api.github.com that records requests.
// token "" leaves the GitHub credential unconfigured.
func newGitHubEnv(t *testing.T, token string) *githubEnv {
	t.Helper()
	ge := &githubEnv{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ge.mu.Lock()
		ge.calls = append(ge.calls, githubCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		ge.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-result")
		fmt.Fprint(w, "upstream ok")
	}))
	t.Cleanup(upstream.Close)

	st := newFakeStore()
	e := &env{t: t, st: st}
	tokenFile := filepath.Join(t.TempDir(), "github_token")
	if token != "" {
		if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.gw = &Gateway{
		Store:            st,
		GitHubTokenFile:  tokenFile,
		GitHubBaseURL:    upstream.URL,
		GitHubAPIBaseURL: upstream.URL + "/api",
	}
	e.inference = httptest.NewServer(e.gw.Handler())
	t.Cleanup(e.inference.Close)
	ge.env = e
	return ge
}

func (ge *githubEnv) addSession(status, repo string) (string, uuid.UUID) {
	secret, id := ge.st.add(session.HarnessClaude, status, 1000, 0)
	ge.st.mu.Lock()
	defer ge.st.mu.Unlock()
	for _, s := range ge.st.sessions {
		if s.ID == id {
			s.Repo = repo
		}
	}
	return secret, id
}

func (ge *githubEnv) taken() []githubCall {
	ge.mu.Lock()
	defer ge.mu.Unlock()
	return append([]githubCall(nil), ge.calls...)
}

func pktLine(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

func pushBody(ref, newOID string) string {
	old := strings.Repeat("a", 40)
	return pktLine("shallow "+strings.Repeat("c", 40)+"\n") +
		pktLine(old+" "+newOID+" "+ref+"\x00report-status side-band-64k\n") + "0000" + "PACK-binary-data"
}

func gitBasic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

func TestGitHubFetchSwapsCredential(t *testing.T) {
	ge := newGitHubEnv(t, realGitHubToken)
	secret, _ := ge.addSession(session.StatusProvisioning, "octo/Hello")

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(secret), "")
	if r.status != http.StatusOK || r.body != "upstream ok" {
		t.Fatalf("info/refs: %d %s", r.status, r.body)
	}
	assertNoSecrets(t, r, realGitHubToken, secret)
	r = ge.do("POST", "/github/octo/Hello/git-upload-pack", basic(secret), "0000")
	if r.status != http.StatusOK {
		t.Fatalf("upload-pack: %d %s", r.status, r.body)
	}
	calls := ge.taken()
	if len(calls) != 2 {
		t.Fatalf("calls: %+v", calls)
	}
	// The session's spec spelling is used upstream, whatever case the sandbox sent.
	if calls[0].path != "/octo/Hello.git/info/refs" || calls[0].query != "service=git-upload-pack" || calls[1].path != "/octo/Hello.git/git-upload-pack" {
		t.Errorf("upstream paths: %+v", calls)
	}
	for _, c := range calls {
		if c.auth != gitBasic(realGitHubToken) {
			t.Errorf("upstream auth %q", c.auth)
		}
	}
}

func TestGitHubChallengesWithoutCredential(t *testing.T) {
	ge := newGitHubEnv(t, realGitHubToken)
	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", "", "")
	if r.status != http.StatusUnauthorized || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("got %d %v", r.status, r.header)
	}
	r = ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(strings.Repeat("f", 64)), "")
	if r.status != http.StatusForbidden || len(ge.taken()) != 0 {
		t.Errorf("unknown secret: %d", r.status)
	}
}

func TestGitHubDenials(t *testing.T) {
	ge := newGitHubEnv(t, realGitHubToken)
	secret, id := ge.addSession(session.StatusRunning, "octo/hello")
	ended, _ := ge.addSession(session.StatusComplete, "octo/hello")
	noRepo, _ := ge.addSession(session.StatusRunning, "")

	cases := []struct {
		name, method, path, auth, body string
		want                           int
	}{
		{"other repo", "GET", "/github/octo/other.git/info/refs?service=git-upload-pack", basic(secret), "", 403},
		{"repo prefix", "GET", "/github/octo/hello-evil.git/info/refs?service=git-upload-pack", basic(secret), "", 403},
		{"extra query", "GET", "/github/octo/hello.git/info/refs?service=git-upload-pack&x=1", basic(secret), "", 403},
		{"other service", "GET", "/github/octo/hello.git/info/refs?service=git-archive", basic(secret), "", 403},
		{"encoded path", "GET", "/github/octo%2Fhello.git/info/refs?service=git-upload-pack", basic(secret), "", 403},
		{"other api", "GET", "/github/api/repos/octo/hello/pulls", basic(secret), "", 403},
		{"other repo api", "POST", "/github/api/repos/octo/other/pulls", basic(secret), `{"head":"x"}`, 403},
		{"api user", "GET", "/github/api/user", basic(secret), "", 403},
		{"ended session", "GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(ended), "", 403},
		{"no repo", "GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(noRepo), "", 403},
		{"push main", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/heads/main", strings.Repeat("b", 40)), 403},
		{"push other session", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
			pushBody("refs/heads/"+session.BranchPrefix(uuid.NewString())+"x", strings.Repeat("b", 40)), 403},
		{"delete own branch", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
			pushBody("refs/heads/"+session.BranchPrefix(id.String())+"x", zeroOID), 403},
		{"push tag", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/tags/v1", strings.Repeat("b", 40)), 403},
		{"malformed push", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), "zzzz", 400},
		{"pr from main", "POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"head":"main","base":"dev"}`, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ge.do(c.method, c.path, c.auth, c.body)
			if r.status != c.want {
				t.Errorf("got %d %s, want %d", r.status, r.body, c.want)
			}
			assertNoSecrets(t, r, realGitHubToken)
		})
	}
	if n := len(ge.taken()); n != 0 {
		t.Errorf("%d requests reached GitHub", n)
	}
	var denied int
	for _, ev := range ge.st.eventsFor(id) {
		if ev["via"] == "github" && ev["decision"] == "denied" {
			denied++
		}
	}
	if denied == 0 {
		t.Error("no denied github egress events recorded")
	}
}

func TestGitHubPushToSessionBranch(t *testing.T) {
	ge := newGitHubEnv(t, realGitHubToken)
	secret, id := ge.addSession(session.StatusRunning, "octo/hello")
	ref := "refs/heads/" + session.BranchPrefix(id.String()) + "fix-readme"
	// Larger than bufio's buffer, so part of the pack is still unread when the commands are checked.
	body := pushBody(ref, strings.Repeat("b", 40)) + strings.Repeat("P", 70000)

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-receive-pack", basic(secret), "")
	if r.status != http.StatusOK {
		t.Fatalf("receive-pack refs: %d %s", r.status, r.body)
	}
	r = ge.do("POST", "/github/octo/hello.git/git-receive-pack", basic(secret), body)
	if r.status != http.StatusOK {
		t.Fatalf("push: %d %s", r.status, r.body)
	}
	calls := ge.taken()
	if len(calls) != 2 || calls[1].body != body || calls[1].auth != gitBasic(realGitHubToken) {
		t.Fatalf("push not forwarded intact: %d calls", len(calls))
	}
	var allowed bool
	for _, ev := range ge.st.eventsFor(id) {
		if ev["via"] == "github" && ev["decision"] == "allowed" {
			allowed = true
		}
	}
	if !allowed {
		t.Error("allowed push not recorded")
	}
}

func TestGitHubPullRequest(t *testing.T) {
	ge := newGitHubEnv(t, realGitHubToken)
	secret, id := ge.addSession(session.StatusIdle, "octo/hello")
	body := `{"title":"t","head":"` + session.BranchPrefix(id.String()) + `fix","base":"main"}`

	r := ge.do("POST", "/github/api/repos/octo/hello/pulls", basic(secret), body)
	if r.status != http.StatusOK {
		t.Fatalf("pr: %d %s", r.status, r.body)
	}
	calls := ge.taken()
	if len(calls) != 1 || calls[0].path != "/api/repos/octo/hello/pulls" || calls[0].body != body || calls[0].auth != "Bearer "+realGitHubToken {
		t.Errorf("upstream: %+v", calls)
	}
}

func TestGitHubWithoutToken(t *testing.T) {
	ge := newGitHubEnv(t, "")
	secret, id := ge.addSession(session.StatusRunning, "octo/hello")

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(secret), "")
	if r.status != http.StatusOK {
		t.Fatalf("public fetch: %d %s", r.status, r.body)
	}
	if calls := ge.taken(); len(calls) != 1 || calls[0].auth != "" {
		t.Errorf("fetch without token sent auth %+v", calls)
	}
	r = ge.do("POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
		pushBody("refs/heads/"+session.BranchPrefix(id.String())+"x", strings.Repeat("b", 40)))
	if r.status != http.StatusServiceUnavailable {
		t.Errorf("push without token: %d", r.status)
	}
	r = ge.do("POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"head":"`+session.BranchPrefix(id.String())+`x"}`)
	if r.status != http.StatusServiceUnavailable {
		t.Errorf("pr without token: %d", r.status)
	}
}
