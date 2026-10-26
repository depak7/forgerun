package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/publish"
	"github.com/depak7/forgerun/internal/remote"
	"github.com/depak7/forgerun/internal/store"
)

type orchestratorConfig struct {
	api         api.Config
	workerToken string
	publisher   *publish.Publisher
	prTokens    forge.TokenSource
}

// orchestrate serves the API and web UI with no workers of its own: turns
// go to "forgerun worker" processes that connect to /api/worker/.
func orchestrate(ctx context.Context, stop context.CancelFunc, ln net.Listener, db store.Store, log *slog.Logger, cfg orchestratorConfig) error {
	rs := &remote.Server{Store: db, Token: cfg.workerToken, OutDir: cfg.api.OutDir, Log: log}
	if cfg.publisher != nil {
		rs.FetchToken = fetchTokens(db, cfg.api.Auth, cfg.publisher, cfg.prTokens, log)
	}
	ac := cfg.api
	ac.Pool, ac.Sessions, ac.Workers = rs, rs, rs
	srv := &http.Server{
		Handler:           routes(os.Getenv("FORGERUN_PUBLIC_URL"), rs, api.LogRequests(log, api.New(ac))),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go rs.Reap(ctx) // fails turns whose worker went away
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("forgerun orchestrator serving; workers connect with forgerun worker", "addr", ln.Addr().String())

	var runErr error
	select {
	case err := <-serveErr:
		runErr = fmt.Errorf("http server: %w", err)
		stop()
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http shutdown", "err", err)
	}
	log.Info("stopped")
	return runErr
}

// fetchTokens decides, per turn, whether the worker must fetch the repo on
// its host (a private repo) and with which GitHub token: the task owner's,
// or the server's own. "" means the VM can clone it.
func fetchTokens(db store.Store, auth *api.Auth, p *publish.Publisher, server forge.TokenSource, log *slog.Logger) func(context.Context, store.Record) (string, error) {
	return func(ctx context.Context, rec store.Record) (string, error) {
		tok, err := auth.Token(ctx, db, rec.Owner)
		if err != nil {
			return "", err
		}
		acc, err := p.Access(ctx, rec.Repo, tok)
		if err != nil {
			log.Warn("checking whether a repo is private", "repo", rec.Repo, "err", err)
			return "", nil // let the VM try a plain clone
		}
		if !acc.Private {
			return "", nil
		}
		if tok.IsZero() && server != nil {
			repo, err := forge.ParseRepo(rec.Repo)
			if err != nil {
				return "", err
			}
			if tok, err = server.Token(ctx, repo); err != nil {
				return "", err
			}
		}
		return tok.Reveal(), nil
	}
}
