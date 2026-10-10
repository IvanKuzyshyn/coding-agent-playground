// Spike S1 gateway: swaps a placeholder Claude OAuth token for the real one and logs every
// request and CONNECT attempt, so we learn exactly what Claude Code needs from the network.
package main

import (
	"crypto/subtle"
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

func main() {
	placeholder := mustEnv("PLACEHOLDER_TOKEN")
	real := mustEnv("REAL_CLAUDE_OAUTH_TOKEN")
	allow := splitList(os.Getenv("CONNECT_ALLOW"))

	go func() {
		slog.Info("connect proxy listening", "addr", ":3128", "allow", allow)
		srv := &http.Server{Addr: ":3128", Handler: connectProxy(allow), ReadHeaderTimeout: 10 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("connect proxy", "err", err)
			os.Exit(1)
		}
	}()

	slog.Info("inference proxy listening", "addr", ":8080")
	srv := &http.Server{Addr: ":8080", Handler: inferenceProxy(placeholder, real), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("inference proxy", "err", err)
		os.Exit(1)
	}
}

var allowedRoutes = map[string]bool{
	"POST /anthropic/v1/messages":              true,
	"POST /anthropic/v1/messages/count_tokens": true,
}

func inferenceProxy(placeholder, real string) http.Handler {
	upstream, _ := url.Parse("https://api.anthropic.com")
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/anthropic")
			r.Out.URL.RawPath = ""
			r.Out.Host = upstream.Host
			r.Out.Header.Set("Authorization", "Bearer "+real)
		},
		FlushInterval: -1, // stream SSE responses without buffering
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
			"auth", auth,
			"anthropic_beta", r.Header.Get("anthropic-beta"),
			"user_agent", r.Header.Get("User-Agent"),
			"headers", headerNames(r.Header),
		)
		// The real token may carry more than inference scope, so only exact inference routes get it.
		// RawPath must be empty so encoded or dot-segment paths can never match.
		if r.URL.RawPath != "" || !allowedRoutes[r.Method+" "+r.URL.Path] {
			slog.Info("inference route denied", "method", r.Method, "path", r.URL.Path)
			http.Error(w, "route not allowed", http.StatusForbidden)
			return
		}
		// Fail closed: only requests carrying the session placeholder get the real credential.
		if auth != "bearer-placeholder" {
			http.Error(w, "missing or unknown credential", http.StatusForbidden)
			return
		}
		r.Header.Del("X-Api-Key")
		rp.ServeHTTP(w, r)
	})
}

// classify reports which credential shape the client sent without logging its value.
func classify(h http.Header, placeholder string) string {
	if v := h.Get("Authorization"); v != "" {
		token, ok := strings.CutPrefix(v, "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(token), []byte(placeholder)) == 1 {
			return "bearer-placeholder"
		}
		return "bearer-other"
	}
	if h.Get("X-Api-Key") != "" {
		return "x-api-key"
	}
	return "none"
}

func connectProxy(allow []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if r.Method != http.MethodConnect {
			slog.Info("egress denied", "kind", "plain-http", "method", r.Method, "url", r.URL.String())
			http.Error(w, "only CONNECT is supported", http.StatusForbidden)
			return
		}
		hostname, port, err := net.SplitHostPort(host)
		if err != nil || port != "443" || !slices.Contains(allow, hostname) {
			slog.Info("egress denied", "kind", "connect", "host", host)
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		slog.Info("egress allowed", "kind", "connect", "host", host)
		upstream, err := net.DialTimeout("tcp", host, 10*time.Second)
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
