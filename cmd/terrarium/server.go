package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"terrarium/internal/api"
	"terrarium/internal/catalog"
	"terrarium/internal/config"
	"terrarium/internal/harness"
	"terrarium/internal/ops"
	"terrarium/internal/sandbox"
	"terrarium/internal/sandboxclient"
	"terrarium/internal/store"
	"terrarium/internal/supervisor"
)

func runServer(ctx context.Context) error {
	cfg, err := config.Load[config.Server]()
	if err != nil {
		return err
	}
	key, err := os.ReadFile(cfg.SessionKeyFile)
	if err != nil {
		return fmt.Errorf("session key: %w", err)
	}
	if key = []byte(strings.TrimSpace(string(key))); len(key) < 32 {
		return errors.New("session key must be at least 32 bytes")
	}
	cat, err := catalog.Load(cfg.PresetsFile)
	if err != nil {
		return err
	}
	engineers, err := api.LoadEngineers(cfg.EngineersFile)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	log := slog.Default()
	o := &ops.Ops{
		Store:     st,
		Sandboxes: sandbox.NewDocker(cfg.DockerSocket, cfg.SandboxNetwork),
		NewClient: func(endpoint, token string) ops.SandboxAPI {
			return sandboxclient.New(endpoint, token, nil)
		},
		Gateway:      harness.Gateway{URL: cfg.GatewayURL, ProxyHost: cfg.GatewayProxy},
		SessionKey:   key,
		Log:          log,
		PollInterval: 500 * time.Millisecond,
		StopGrace:    10 * time.Second,
	}
	sup := &supervisor.Supervisor{Store: st, Ops: o, Log: log, Interval: 5 * time.Second, IdlePoll: time.Second}
	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: (&api.Server{
			Store: st, Catalog: cat, Engineers: engineers, SessionKey: key,
			MaxActive: cfg.MaxActiveSessionsPerOwner, Log: log, EventPoll: 500 * time.Millisecond,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return sup.Run(gctx) })
	g.Go(func() error {
		log.Info("api listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}
