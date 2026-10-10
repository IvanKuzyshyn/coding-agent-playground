// Spike S2 gateway: Codex in ChatGPT-subscription mode runs on forged placeholder tokens; the
// gateway swaps in the real access token and account id, and answers token refreshes itself so
// the real refresh token never leaves the gateway.
package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

type realAuth struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

func main() {
	placeholder := mustEnv("PLACEHOLDER_ACCESS_TOKEN")
	placeholderAuth := mustEnv("PLACEHOLDER_AUTH_JSON_PATH")
	real := loadRealAuth(mustEnv("REAL_AUTH_JSON_PATH"))
	allow := splitList(os.Getenv("CONNECT_ALLOW"))

	go func() {
		slog.Info("connect proxy listening", "addr", ":3128", "allow", allow)
		srv := &http.Server{Addr: ":3128", Handler: connectProxy(allow), ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("connect proxy", "err", err)
			os.Exit(1)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/openai/", inferenceProxy(placeholder, real))
	mux.Handle("/oauth/", refreshStub(placeholderAuth))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("unknown route denied", "method", r.Method, "path", r.URL.Path)
		http.Error(w, "route not allowed", http.StatusForbidden)
	})

	slog.Info("gateway listening", "addr", ":8080")
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("gateway", "err", err)
		os.Exit(1)
	}
}

func loadRealAuth(path string) realAuth {
	b, err := os.ReadFile(path)
	if err != nil {
		slog.Error("read real auth", "err", err)
		os.Exit(1)
	}
	var a realAuth
	if err := json.Unmarshal(b, &a); err != nil || a.Tokens.AccessToken == "" || a.Tokens.AccountID == "" {
		slog.Error("real auth.json missing tokens.access_token or tokens.account_id")
		os.Exit(1)
	}
	return a
}

// Exact routes the real token may be used for, relative to /openai. Anything else under
// chatgpt.com/backend-api could reach account APIs with the subscriber's session.
var allowedRoutes = map[string]bool{
	"POST /openai/responses": true,
	"GET /openai/models":     true,
}

func inferenceProxy(placeholder string, real realAuth) http.Handler {
	upstream, _ := url.Parse("https://chatgpt.com")
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.URL.Path = "/backend-api/codex" + strings.TrimPrefix(r.In.URL.Path, "/openai")
			r.Out.URL.RawPath = ""
			r.Out.Host = upstream.Host
			r.Out.Header.Set("Authorization", "Bearer "+real.Tokens.AccessToken)
			r.Out.Header.Set("ChatGPT-Account-ID", real.Tokens.AccountID)
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			slog.Info("upstream response", "method", resp.Request.Method, "path", resp.Request.URL.Path, "status", resp.StatusCode)
			return nil
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := classify(r.Header, placeholder)
		slog.Info("inference request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"auth", auth,
			"upgrade", r.Header.Get("Upgrade"),
			"user_agent", r.Header.Get("User-Agent"),
			"headers", headerNames(r.Header),
		)
		if r.URL.RawPath != "" || !allowedRoutes[r.Method+" "+r.URL.Path] {
			slog.Info("inference route denied", "method", r.Method, "path", r.URL.Path)
			http.Error(w, "route not allowed", http.StatusForbidden)
			return
		}
		if auth != "bearer-placeholder" {
			http.Error(w, "missing or unknown credential", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// refreshStub answers Codex's token refresh with the same placeholder tokens. The real refresh
// happens only inside the gateway (not implemented in this spike).
func refreshStub(placeholderAuthPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("refresh request", "method", r.Method, "path", r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" {
			http.Error(w, "route not allowed", http.StatusForbidden)
			return
		}
		b, err := os.ReadFile(placeholderAuthPath)
		if err != nil {
			http.Error(w, "no placeholder", http.StatusInternalServerError)
			return
		}
		var a struct {
			Tokens map[string]string `json:"tokens"`
		}
		if err := json.Unmarshal(b, &a); err != nil {
			http.Error(w, "bad placeholder", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id_token":      a.Tokens["id_token"],
			"access_token":  a.Tokens["access_token"],
			"refresh_token": a.Tokens["refresh_token"],
		})
	})
}

func classify(h http.Header, placeholder string) string {
	if v := h.Get("Authorization"); v != "" {
		token, ok := strings.CutPrefix(v, "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(token), []byte(placeholder)) == 1 {
			return "bearer-placeholder"
		}
		return "bearer-other"
	}
	return "none"
}

func connectProxy(allow []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			slog.Info("egress denied", "kind", "plain-http", "method", r.Method, "url", r.URL.String())
			http.Error(w, "only CONNECT is supported", http.StatusForbidden)
			return
		}
		hostname, port, err := net.SplitHostPort(r.Host)
		if err != nil || port != "443" || !slices.Contains(allow, hostname) {
			slog.Info("egress denied", "kind", "connect", "host", r.Host)
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		slog.Info("egress allowed", "kind", "connect", "host", r.Host)
		upstream, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		client, buf, err := hj.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		// The client may have sent the start of its TLS handshake before reading our 200.
		if n := buf.Reader.Buffered(); n > 0 {
			early, _ := buf.Reader.Peek(n)
			_, _ = upstream.Write(early)
		}
		go pipe(upstream, client)
		pipe(client, upstream)
	})
}

func pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

func headerNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing env", "key", k)
		os.Exit(1)
	}
	return v
}
