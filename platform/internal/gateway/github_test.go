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
	. "github.com/onsi/gomega"

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

var (
	none     = strings.Repeat("0", 40) // the ref does not exist on that side
	existing = strings.Repeat("a", 40)
	newer    = strings.Repeat("b", 40)
)

func pushBody(ref, old, newOID string) string {
	return pktLine("shallow "+strings.Repeat("c", 40)+"\n") +
		pktLine(old+" "+newOID+" "+ref+"\x00report-status side-band-64k\n") + "0000" + "PACK-binary-data"
}

func gitBasic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

func TestGitHubFetchSwapsCredential(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, realGitHubToken)
	secret, _ := ge.addSession(session.StatusProvisioning, "octo/Hello")

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(secret), "")
	g.Expect(r.status).To(Equal(http.StatusOK), "info/refs: %s", r.body)
	g.Expect(r.body).To(Equal("upstream ok"))
	assertNoSecrets(t, r, realGitHubToken, secret)
	r = ge.do("POST", "/github/octo/Hello/git-upload-pack", basic(secret), "0000")
	g.Expect(r.status).To(Equal(http.StatusOK), "upload-pack: %s", r.body)
	calls := ge.taken()
	g.Expect(calls).To(HaveLen(2))
	// The session's spec spelling is used upstream, whatever case the sandbox sent.
	g.Expect(calls[0].path).To(Equal("/octo/Hello.git/info/refs"))
	g.Expect(calls[0].query).To(Equal("service=git-upload-pack"))
	g.Expect(calls[1].path).To(Equal("/octo/Hello.git/git-upload-pack"))
	for _, c := range calls {
		g.Expect(c.auth).To(Equal(gitBasic(realGitHubToken)), "upstream auth")
	}
}

func TestGitHubChallengesWithoutCredential(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, realGitHubToken)
	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", "", "")
	g.Expect(r.status).To(Equal(http.StatusUnauthorized))
	g.Expect(r.header.Get("WWW-Authenticate")).To(HavePrefix("Basic"))
	r = ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(strings.Repeat("f", 64)), "")
	g.Expect(r.status).To(Equal(http.StatusForbidden), "unknown secret")
	g.Expect(ge.taken()).To(BeEmpty(), "unknown secret reached GitHub")
}

func TestGitHubDenials(t *testing.T) {
	g := NewWithT(t)
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
		{"create unprefixed branch", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/heads/feature", none, newer), 403},
		{"create other session's branch", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
			pushBody("refs/heads/"+session.BranchPrefix(uuid.NewString())+"x", none, newer), 403},
		{"delete branch", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/heads/feature", existing, none), 403},
		{"delete own branch", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
			pushBody("refs/heads/"+session.BranchPrefix(id.String())+"x", existing, none), 403},
		{"push tag", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/tags/v1", none, newer), 403},
		{"update tag", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody("refs/tags/v1", existing, newer), 403},
		{"malformed push", "POST", "/github/octo/hello.git/git-receive-pack", basic(secret), "zzzz", 400},
		{"pr from a fork", "POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"head":"attacker:main","base":"main"}`, 403},
		{"pr without head", "POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"base":"main"}`, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ge.do(c.method, c.path, c.auth, c.body)
			NewWithT(t).Expect(r.status).To(Equal(c.want), "body: %s", r.body)
			assertNoSecrets(t, r, realGitHubToken)
		})
	}
	g.Expect(ge.taken()).To(BeEmpty(), "requests reached GitHub")
	var denied int
	for _, ev := range ge.st.eventsFor(id) {
		if ev["via"] == "github" && ev["decision"] == "denied" {
			denied++
		}
	}
	g.Expect(denied).NotTo(BeZero(), "no denied github egress events recorded")
}

func TestGitHubPushToSessionBranch(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, realGitHubToken)
	secret, id := ge.addSession(session.StatusRunning, "octo/hello")
	ref := "refs/heads/" + session.BranchPrefix(id.String()) + "fix-readme"
	// Larger than bufio's buffer, so part of the pack is still unread when the commands are checked.
	body := pushBody(ref, none, newer) + strings.Repeat("P", 70000)

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-receive-pack", basic(secret), "")
	g.Expect(r.status).To(Equal(http.StatusOK), "receive-pack refs: %s", r.body)
	r = ge.do("POST", "/github/octo/hello.git/git-receive-pack", basic(secret), body)
	g.Expect(r.status).To(Equal(http.StatusOK), "push: %s", r.body)
	calls := ge.taken()
	g.Expect(calls).To(HaveLen(2))
	// Compare as a bool: printing the ~70KB body on failure is noise.
	g.Expect(calls[1].body == body).To(BeTrue(), "push not forwarded intact")
	g.Expect(calls[1].auth).To(Equal(gitBasic(realGitHubToken)))
	var allowed bool
	for _, ev := range ge.st.eventsFor(id) {
		if ev["via"] == "github" && ev["decision"] == "allowed" {
			allowed = true
		}
	}
	g.Expect(allowed).To(BeTrue(), "allowed push not recorded")
}

func TestGitHubPullRequest(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, realGitHubToken)
	secret, id := ge.addSession(session.StatusIdle, "octo/hello")
	body := `{"title":"t","head":"` + session.BranchPrefix(id.String()) + `fix","base":"main"}`

	r := ge.do("POST", "/github/api/repos/octo/hello/pulls", basic(secret), body)
	g.Expect(r.status).To(Equal(http.StatusOK), "pr: %s", r.body)
	calls := ge.taken()
	g.Expect(calls).To(HaveLen(1))
	g.Expect(calls[0].path).To(Equal("/api/repos/octo/hello/pulls"))
	g.Expect(calls[0].body).To(Equal(body))
	g.Expect(calls[0].auth).To(Equal("Bearer " + realGitHubToken))
}

func TestGitHubWithoutToken(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, "")
	secret, id := ge.addSession(session.StatusRunning, "octo/hello")

	r := ge.do("GET", "/github/octo/hello.git/info/refs?service=git-upload-pack", basic(secret), "")
	g.Expect(r.status).To(Equal(http.StatusOK), "public fetch: %s", r.body)
	calls := ge.taken()
	g.Expect(calls).To(HaveLen(1))
	g.Expect(calls[0].auth).To(BeEmpty(), "fetch without token sent auth")
	r = ge.do("POST", "/github/octo/hello.git/git-receive-pack", basic(secret),
		pushBody("refs/heads/"+session.BranchPrefix(id.String())+"x", none, newer))
	g.Expect(r.status).To(Equal(http.StatusServiceUnavailable), "push without token")
	r = ge.do("POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"head":"`+session.BranchPrefix(id.String())+`x"}`)
	g.Expect(r.status).To(Equal(http.StatusServiceUnavailable), "pr without token")
}

func TestGitHubPushUpdatesExistingBranches(t *testing.T) {
	g := NewWithT(t)
	ge := newGitHubEnv(t, realGitHubToken)
	secret, _ := ge.addSession(session.StatusRunning, "octo/hello")
	for _, ref := range []string{"refs/heads/add-feature", "refs/heads/main"} {
		r := ge.do("POST", "/github/octo/hello.git/git-receive-pack", basic(secret), pushBody(ref, existing, newer))
		g.Expect(r.status).To(Equal(http.StatusOK), "update %s: %s", ref, r.body)
	}
	// A PR may come from any branch of the repository.
	r := ge.do("POST", "/github/api/repos/octo/hello/pulls", basic(secret), `{"head":"add-feature","base":"main"}`)
	g.Expect(r.status).To(Equal(http.StatusOK), "pr from existing branch: %s", r.body)
	g.Expect(ge.taken()).To(HaveLen(3), "requests reached GitHub")
}

func TestPushAllowedSHA256(t *testing.T) {
	zero256 := strings.Repeat("0", 64)
	msg := pushAllowed(pushCommand{zero256, strings.Repeat("b", 64), "refs/heads/feature"}, "terrarium/s/")
	NewWithT(t).Expect(msg).NotTo(BeEmpty(), "SHA-256 zero id not treated as a new branch")
}
