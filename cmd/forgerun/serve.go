package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store/postgres"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/worker"
)

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	workers := fs.Int("workers", 2, "how many tasks may run at once")
	timeout := fs.Duration("timeout", 15*time.Minute, "max time per task")
	out := fs.String("out", "out", "directory for task results")
	dbURL := dbFlag(fs)
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Ctrl-C / SIGTERM start a graceful shutdown; a second one force-quits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	ag, err := agent.NewCursor(os.Getenv("CURSOR_API_KEY"))
	if err != nil {
		return err
	}
	db, err := postgres.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := sandbox.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("microsandbox runtime: %w", err)
	}

	// Listen before starting anything else, so "port in use" fails fast.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	if !isLoopback(*addr) {
		log.Warn("listening on a non-loopback address: the API has no authentication yet", "addr", *addr)
	}

	// Tasks left "running" by a crash have no worker anymore.
	if n, err := db.FailRunning(ctx, "server restarted"); err != nil {
		ln.Close()
		return err
	} else if n > 0 {
		log.Warn("marked orphaned running tasks as failed", "count", n)
	}

	runner := &task.Runner{Agent: ag, OutDir: *out, CPUs: 2, MemMiB: 2048, Timeout: *timeout, Stdout: io.Discard}
	pool := worker.New(db, runner, *workers, log)
	srv := &http.Server{
		Handler:           api.LogRequests(log, api.New(db, pool, ag.Name(), log)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	poolDone := make(chan struct{})
	go func() {
		pool.Run(ctx)
		close(poolDone)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("forgerun serving", "addr", ln.Addr().String(), "workers", *workers)

	var runErr error
	select {
	case err := <-serveErr: // the server died on its own
		runErr = fmt.Errorf("http server: %w", err)
		stop()
	case <-ctx.Done():
		log.Info("shutting down: finishing requests, stopping running tasks")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http shutdown", "err", err)
	}
	<-poolDone // every running task has recorded its outcome and destroyed its VM
	log.Info("stopped")
	return runErr
}

// isLoopback reports whether addr only accepts connections from this machine.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
