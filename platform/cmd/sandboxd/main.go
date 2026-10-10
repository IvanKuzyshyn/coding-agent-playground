// Command sandboxd is the exec daemon inside each sandbox container. The supervisor drives the
// sandbox through its HTTP API. See docs/contracts/sandboxd.md.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const tokenEnv = "SANDBOXD_TOKEN"

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var addr, home string
	cmd := &cobra.Command{
		Use:   "sandboxd",
		Short: "Exec daemon inside a Terrarium sandbox",
		Long: "sandboxd is the first process of every sandbox container. It serves the supervisor's HTTP API\n" +
			"(files, commands, logs, signals; docs/contracts/sandboxd.md), authenticated with the token in\n" +
			tokenEnv + ", which it removes from its environment before starting any command.",
		Args: cobra.NoArgs,
		// Usage is printed for mistakes in the command line, not for failures while running.
		PreRun: func(cmd *cobra.Command, _ []string) { cmd.SilenceUsage = true },
		RunE: func(*cobra.Command, []string) error {
			hardenProcess()
			if os.Getpid() == 1 {
				os.Exit(runInit())
			}
			return serve(addr, home)
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.Flags().StringVar(&addr, "addr", cmp.Or(os.Getenv("SANDBOXD_ADDR"), ":7777"), "listen address (env SANDBOXD_ADDR)")
	cmd.Flags().StringVar(&home, "home", cmp.Or(os.Getenv("SANDBOXD_HOME"), "/home/agent"),
		"base directory holding .sandboxd and work (env SANDBOXD_HOME)")
	return cmd
}

func serve(addr, home string) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	token := takeToken()
	if token == "" {
		return errors.New(tokenEnv + " is not set")
	}
	stateDir, workDir := filepath.Join(home, ".sandboxd"), filepath.Join(home, "work")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

	s := newServer(token, stateDir, workDir)
	srv := &http.Server{Addr: addr, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
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
	return nil
}

// takeToken reads the token and removes it from the environment so child commands never see it.
func takeToken() string {
	token := os.Getenv(tokenEnv)
	os.Unsetenv(tokenEnv)
	return token
}
