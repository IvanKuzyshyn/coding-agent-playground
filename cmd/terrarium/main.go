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

	"terrarium/internal/config"
	"terrarium/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: terrarium server|gateway|migrate")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(ctx)
	case "gateway":
		err = runGateway(ctx)
	case "migrate":
		err = runMigrate(ctx)
	case "healthcheck":
		// Distroless images have no curl; compose healthchecks call the binary instead.
		err = runHealthcheck(ctx, os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		slog.Error("exit", "cmd", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func runHealthcheck(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: terrarium healthcheck <url>")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, args[0], nil)
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
