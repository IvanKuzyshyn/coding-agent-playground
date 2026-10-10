package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"terrarium/internal/config"
	"terrarium/internal/gateway"
	"terrarium/internal/store"
)

func runGateway(ctx context.Context) error {
	cfg, err := config.Load[config.Gateway]()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	gw := &gateway.Gateway{
		Store:           st,
		ClaudeTokenFile: cfg.ClaudeTokenFile,
		CodexAuthFile:   cfg.CodexAuthFile,
	}
	servers := []*http.Server{
		{Addr: cfg.Addr, Handler: gw.Handler(), ReadHeaderTimeout: 10 * time.Second},
		{Addr: cfg.ProxyAddr, Handler: gw.ProxyHandler(), ReadHeaderTimeout: 10 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			slog.Info("gateway listening", "addr", srv.Addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		// Shutdown does not track hijacked CONNECT tunnels; they end with the process.
		if serr := srv.Shutdown(shutdownCtx); serr != nil {
			srv.Close()
		}
	}
	return err
}
