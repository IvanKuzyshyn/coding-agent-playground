// Command sandboxd is the exec daemon inside each sandbox container. The supervisor drives the
// sandbox through its HTTP API. See docs/contracts/sandboxd.md.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const tokenEnv = "SANDBOXD_TOKEN"

func main() {
	addr := flag.String("addr", cmp.Or(os.Getenv("SANDBOXD_ADDR"), ":7777"), "listen address")
	home := flag.String("home", cmp.Or(os.Getenv("SANDBOXD_HOME"), "/home/agent"), "base directory holding .sandboxd and work")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	hardenProcess()
	if os.Getpid() == 1 {
		os.Exit(runInit())
	}

	token := takeToken()
	if token == "" {
		log.Error(tokenEnv + " is not set")
		os.Exit(1)
	}
	stateDir, workDir := filepath.Join(*home, ".sandboxd"), filepath.Join(*home, "work")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Error("create state dir", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		log.Error("create work dir", "err", err)
		os.Exit(1)
	}

	s := newServer(token, stateDir, workDir)
	srv := &http.Server{Addr: *addr, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", *addr)

	select {
	case err := <-errc:
		log.Error("serve", "err", err)
		os.Exit(1)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Error("shutdown", "err", err)
	}
	// Docker sends KILL 10s after TERM by default; leave headroom.
	s.terminateAll(6 * time.Second)
}

// takeToken reads the token and removes it from the environment so child commands never see it.
func takeToken() string {
	token := os.Getenv(tokenEnv)
	os.Unsetenv(tokenEnv)
	return token
}
