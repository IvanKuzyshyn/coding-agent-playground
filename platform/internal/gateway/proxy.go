package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"terrarium/internal/credentials"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// ProxyHandler serves the CONNECT egress proxy.
func (g *Gateway) ProxyHandler() http.Handler {
	g.init()
	return http.HandlerFunc(g.serveConnect)
}

func (g *Gateway) serveConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	host := truncate(r.Host)
	auth := r.Header.Get("Proxy-Authorization")
	if r.Method != http.MethodConnect {
		slog.Info("egress denied", "reason", "method", "method", r.Method, "host", host)
		if secret, ok := proxySecret(auth); ok {
			if s, err := g.Store.GatewaySessionBySecretHash(ctx, credentials.Hash(secret)); err == nil {
				g.denyConnect(ctx, s, host, "method")
			}
		}
		writeError(w, http.StatusForbidden, "only CONNECT is supported")
		return
	}
	if auth == "" {
		w.Header().Set("Proxy-Authenticate", `Basic realm="terrarium"`)
		writeError(w, http.StatusProxyAuthRequired, "proxy authentication required")
		return
	}
	secret, ok := proxySecret(auth)
	if !ok {
		writeError(w, http.StatusForbidden, "missing or unknown credential")
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
	if !session.EgressAllowed(s.Status) {
		g.denyConnect(ctx, s, host, "status")
		writeError(w, http.StatusForbidden, "session not active")
		return
	}
	hostname, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		g.denyConnect(ctx, s, host, "port")
		writeError(w, http.StatusForbidden, "host not allowed")
		return
	}
	if !slices.Contains(s.Allow, hostname) {
		g.denyConnect(ctx, s, host, "host")
		writeError(w, http.StatusForbidden, "host not allowed")
		return
	}

	slog.Info("egress allowed", "session", s.ID, "host", host)
	g.event(ctx, s.ID, map[string]any{"decision": "allowed", "via": "connect", "host": host})
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	upstream, err := g.Dial(dialCtx, "tcp", r.Host)
	cancel()
	if err != nil {
		slog.Error("egress dial", "session", s.ID, "host", host, "err", err)
		writeError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		writeError(w, http.StatusInternalServerError, "hijack unsupported")
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
}

func (g *Gateway) denyConnect(ctx context.Context, s store.GatewaySession, host, reason string) {
	slog.Info("egress denied", "session", s.ID, "host", host, "reason", reason)
	g.event(ctx, s.ID, map[string]any{"decision": "denied", "via": "connect", "host": host, "reason": reason})
}

// proxySecret extracts the secret from Basic base64("terrarium:<secret>").
func proxySecret(auth string) (string, bool) {
	enc, ok := strings.CutPrefix(auth, "Basic ")
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return "", false
	}
	user, pass, ok := strings.Cut(string(b), ":")
	if !ok || user != credentials.ProxyUser || pass == "" {
		return "", false
	}
	return pass, true
}

func pipe(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}
