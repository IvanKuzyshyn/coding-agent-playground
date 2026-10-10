// Command terrarium runs the control-plane processes: server (API + supervisor), gateway, migrate.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"terrarium/internal/config"
	"terrarium/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := rootCmd().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "terrarium",
		Short: "Terrarium control plane: API server, credential gateway and migrations",
		Long: "Terrarium runs coding agents in disposable sandboxes. This binary runs the control-plane\n" +
			"processes; each is configured through TERRARIUM_* environment variables (internal/config).",
		// Usage is printed for mistakes in the command line, not for failures while running.
		PersistentPreRun: func(cmd *cobra.Command, _ []string) { cmd.SilenceUsage = true },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		&cobra.Command{
			Use:   "server",
			Short: "Run the API, MCP endpoint and the session supervisor",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return logged(cmd, runServer(cmd.Context())) },
		},
		&cobra.Command{
			Use:   "gateway",
			Short: "Run the credential gateway and the sandboxes' egress proxy",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return logged(cmd, runGateway(cmd.Context())) },
		},
		&cobra.Command{
			Use:   "migrate",
			Short: "Apply pending database migrations",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return logged(cmd, runMigrate(cmd.Context())) },
		},
		&cobra.Command{
			Use:   "healthcheck <url>",
			Short: "Exit 0 if GET <url> answers 200 (container healthchecks; images have no curl)",
			Args:  cobra.ExactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return runHealthcheck(cmd.Context(), args[0]) },
		},
	)
	return root
}

// logged records a failure of a long-running process as a JSON log line, like the rest of its
// output, before cobra prints it.
func logged(cmd *cobra.Command, err error) error {
	if err != nil {
		slog.Error("exit", "cmd", cmd.Name(), "err", err)
	}
	return err
}

func runHealthcheck(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func runMigrate(ctx context.Context) error {
	cfg, err := config.Load[config.Migrate]()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	slog.Info("migrations", "applied", applied)
	return err
}
