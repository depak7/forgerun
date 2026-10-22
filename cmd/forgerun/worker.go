package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/remote"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/snapshots"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/worker"
)

// workerCmd runs turns for an orchestrator elsewhere ("forgerun server"):
// it dials out, so nothing needs opening on this machine. Turns wait in the
// orchestrator's queue while no worker is connected.
func workerCmd(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	server := fs.String("server", os.Getenv("FORGERUN_SERVER"), "the orchestrator's address, e.g. https://forgerun.example.com (env FORGERUN_SERVER)")
	host, _ := os.Hostname()
	name := fs.String("name", host, "this worker's name in the orchestrator's settings")
	workers := fs.Int("workers", 2, "how many turns may run at once")
	timeout := fs.Duration("timeout", 15*time.Minute, "max time per turn")
	sessionIdle := fs.Duration("session-idle", 30*time.Minute, "close a thread's session (its VM) after this long without a message (0 = never)")
	maxSessions := fs.Int("sessions", 4, "most sessions open at once (0 = no limit)")
	agentIdle := fs.Duration("agent-idle", 10*time.Minute, "stop an agent that prints nothing and changes no files for this long (0 = never)")
	out := fs.String("out", "out", "directory for this machine's copy of turns' logs")
	home, _ := os.UserHomeDir()
	state := fs.String("state", filepath.Join(home, ".forgerun", "worker"), "where this worker keeps its record of snapshots")
	useSnapshots := fs.Bool("snapshots", true, "open sessions from their repo's snapshot, and save one when a session closes")
	snapshotMaxAge := fs.Duration("snapshot-max-age", 7*24*time.Hour, "delete a repo's snapshot when unused for this long (0 = never)")
	snapshotBudget := byteSize(20e9)
	fs.Var(&snapshotBudget, "snapshot-budget", "total disk for snapshots, e.g. 20GB (0 = no limit)")
	snapshotSweep := fs.Duration("snapshot-sweep", 10*time.Minute, "how often to check snapshot limits and clean up")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	token := os.Getenv("FORGERUN_WORKER_TOKEN")
	switch {
	case *server == "":
		return errors.New("forgerun worker needs -server (or FORGERUN_SERVER): the orchestrator's address")
	case token == "":
		return errors.New("forgerun worker needs FORGERUN_WORKER_TOKEN, the same as the orchestrator's")
	case !strings.HasPrefix(*server, "http://") && !strings.HasPrefix(*server, "https://"):
		return fmt.Errorf("-server %q must start with https:// (or http:// on a trusted network)", *server)
	}
	if strings.HasPrefix(*server, "http://") && !strings.Contains(*server, "localhost") && !strings.Contains(*server, "127.0.0.1") {
		log.Warn("-server is plain http: the worker token and users' GitHub tokens travel unencrypted", "server", *server)
	}
	if *maxSessions > 0 && *maxSessions < *workers {
		*maxSessions = *workers
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C now force-quits
	}()

	// Agent keys live on the worker: the orchestrator never needs them.
	agents := agent.NewSet(agent.EnvCredentials{}, agent.Builtin()...)
	// What it tells the orchestrator: the agents it has keys for, and every
	// agent it can run, for models that need no key (OpenCode's free ones).
	var ids []string
	for _, id := range agents.IDs() {
		if err := agents.Check(ctx, id); err != nil {
			log.Warn("agent has no credential on this worker; only its models that need no key run here", "agent", id, "err", err)
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		log.Warn("this worker has no agent keys: it runs only models that need none")
	}
	if err := sandbox.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("microsandbox runtime: %w", err)
	}

	client := &remote.Client{URL: *server, Token: token, Name: *name, Log: log}
	runner := &task.Runner{Agents: agents, DefaultAgent: cmp.Or(firstOf(ids), "cursor"), OutDir: *out, CPUs: 2, MemMiB: 2048,
		Timeout: *timeout, AgentIdle: *agentIdle, Stdout: io.Discard, Log: log, SessionIdle: *sessionIdle, MaxSessions: *maxSessions,
		LogSink: client.LogSink, VMPrefix: workerVMPrefix}
	removeLeftoverVMs(ctx, workerVMPrefix, log)
	sweepsDone := make(chan struct{})
	if *useSnapshots {
		fstore, err := snapshots.OpenFileStore(filepath.Join(*state, "snapshots.json"))
		if err != nil {
			return err
		}
		// Its own prefix: a worker and an all-in-one server on one machine
		// never sweep each other's snapshots.
		mgr := &snapshots.Manager{Store: fstore, Prefix: "forgerunw", Limits: snapshots.Limits{MaxAge: *snapshotMaxAge, Budget: int64(snapshotBudget)}}
		runner.Snapshots = mgr
		mgr.SweepAndLog(ctx, log)
		go func() {
			mgr.RunSweeps(ctx, *snapshotSweep, log)
			close(sweepsDone)
		}()
	} else {
		close(sweepsDone)
	}

	pool := worker.New(client, runner, *workers, log)
	pool.Fetchers = client.Fetchers
	heartbeatsDone := make(chan struct{})
	go func() {
		client.Heartbeats(ctx, pool, runner, ids, agents.IDs(), *maxSessions)
		close(heartbeatsDone)
	}()
	go runner.RunSessions(ctx)
	log.Info("forgerun worker started", "server", *server, "name", *name, "workers", *workers, "agents", strings.Join(ids, ","))

	pool.Run(ctx) // returns once every running turn has reported
	log.Info("closing open sessions")
	runner.CloseAll()
	<-sweepsDone
	<-heartbeatsDone
	log.Info("stopped")
	return nil
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// VM name prefixes: a worker and an all-in-one server can share a machine,
// and each removes only its own leftovers.
const (
	serveVMPrefix  = "fs-"
	workerVMPrefix = "fw-"
)

// removeLeftoverVMs removes VMs a previous run left behind (it crashed or
// was killed); their sessions' work is in the database.
func removeLeftoverVMs(ctx context.Context, prefix string, log *slog.Logger) {
	n, err := sandbox.RemoveLeftovers(ctx, prefix)
	if err != nil {
		log.Warn("removing leftover VMs", "err", err)
	}
	if n > 0 {
		log.Info("removed VMs a previous run left behind", "count", n)
	}
}
