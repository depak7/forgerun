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
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/api"
	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/publish"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/snapshots"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/postgres"
	"github.com/depak7/forgerun/internal/task"
	"github.com/depak7/forgerun/internal/webui"
	"github.com/depak7/forgerun/internal/worker"
)

func serveCmd(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	workers := fs.Int("workers", 2, "how many tasks may run at once")
	timeout := fs.Duration("timeout", 15*time.Minute, "max time per turn")
	sessionIdle := fs.Duration("session-idle", 30*time.Minute, "close a thread's session (its VM) after this long without a message (0 = never)")
	maxSessions := fs.Int("sessions", 4, "most sessions open at once; the least recently used idle one closes to make room (0 = no limit)")
	agentIdle := fs.Duration("agent-idle", 10*time.Minute, "stop an agent that prints nothing and changes no files for this long (0 = never)")
	out := fs.String("out", "out", "directory for task results")
	defaultAgent := fs.String("agent", "cursor", "agent for requests that name none")
	defaultModel := fs.String("model", "", "default model family of the default agent (default: the agent's own)")
	requireZDR := fs.Bool("require-zdr", false, "offer only models with zero data retention")
	useSnapshots := fs.Bool("snapshots", true, "open sessions from their repo's snapshot, and save one when a session closes")
	snapshotMaxAge := fs.Duration("snapshot-max-age", 7*24*time.Hour, "delete a repo's snapshot when no task has used it for this long (0 = never)")
	snapshotBudget := byteSize(20e9)
	fs.Var(&snapshotBudget, "snapshot-budget", "total disk for snapshots, e.g. 500MB or 20GB; least recently used are deleted first (0 = no limit)")
	snapshotSweep := fs.Duration("snapshot-sweep", 10*time.Minute, "how often to check snapshot limits and clean up")
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

	// Every agent forgerun has an adapter for. Requests choose among them;
	// credentials are read from the environment only when a task needs one.
	agents := agent.NewSet(agent.EnvCredentials{}, agent.Builtin()...)
	db, err := postgres.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := checkAgent(ctx, db, *defaultAgent); err != nil {
		return err
	}
	if _, err := agents.Get(*defaultAgent); err != nil {
		return fmt.Errorf("-agent: %w", err)
	}
	effective, families, err := checkModels(ctx, db, *defaultAgent, *defaultModel, *requireZDR)
	if err != nil {
		return err
	}
	log.Info("models loaded", "default_agent", *defaultAgent, "families", families, "default", effective, "require_zdr", *requireZDR)
	if err := logAgents(ctx, db, agents, log); err != nil {
		return err
	}
	prTokens, githubUser, err := githubTokens(ctx, os.Getenv("GITHUB_TOKEN"), &forge.GitHub{}, time.Now(), log)
	if err != nil {
		return err
	}
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

	if *maxSessions > 0 && *maxSessions < *workers {
		// Every running turn holds a session: fewer would starve the workers.
		log.Warn("-sessions is below -workers; raising it", "sessions", *maxSessions, "workers", *workers)
		*maxSessions = *workers
	}
	runner := &task.Runner{Agents: agents, DefaultAgent: *defaultAgent, OutDir: *out, CPUs: 2, MemMiB: 2048, Timeout: *timeout,
		AgentIdle: *agentIdle, Stdout: io.Discard, Log: log, SessionIdle: *sessionIdle, MaxSessions: *maxSessions}
	var prs api.PRs // stays a nil interface when PRs are off (a nil *Publisher in it wouldn't be)
	var publisher *publish.Publisher
	if prTokens != nil {
		// Publishing runs git on this host (never in a VM).
		if _, err := exec.LookPath("git"); err != nil {
			ln.Close()
			return fmt.Errorf("pull requests need git installed on this host: %w", err)
		}
		publisher = &publish.Publisher{Tokens: prTokens, GitHub: &forge.GitHub{}}
		prs = publisher
	}
	sweepsDone := make(chan struct{})
	var snapshotSettings *api.SnapshotSettings // nil = snapshots off
	if *useSnapshots {
		snapshotSettings = &api.SnapshotSettings{BudgetBytes: int64(snapshotBudget), MaxAge: *snapshotMaxAge, SweepEvery: *snapshotSweep}
		mgr := &snapshots.Manager{Store: db, Limits: snapshots.Limits{MaxAge: *snapshotMaxAge, Budget: int64(snapshotBudget)}}
		runner.Snapshots = mgr
		// Before any task runs: finish deletes a crash interrupted, remove
		// orphans, apply the limits.
		mgr.SweepAndLog(ctx, log)
		go func() {
			mgr.RunSweeps(ctx, *snapshotSweep, log)
			close(sweepsDone)
		}()
		log.Info("snapshots on", "max_age", *snapshotMaxAge, "budget", snapshotBudget.String(), "sweep_every", *snapshotSweep)
	} else {
		close(sweepsDone)
	}
	pool := worker.New(db, runner, *workers, log)
	srv := &http.Server{
		Handler: api.LogRequests(log, routes(api.New(api.Config{Store: db, Pool: pool, Agents: agents, Agent: *defaultAgent,
			DefaultModel: *defaultModel, RequireZDR: *requireZDR, PRs: prs, Sessions: runner, OutDir: *out, GitHubUser: githubUser,
			Snapshots: snapshotSettings, SessionIdle: *sessionIdle, MaxSessions: *maxSessions, Log: log}))),
		ReadHeaderTimeout: 10 * time.Second,
	}

	poolDone := make(chan struct{})
	go func() {
		pool.Run(ctx)
		close(poolDone)
	}()
	go runner.RunSessions(ctx) // closes idle sessions
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
	<-poolDone // every running turn has recorded its outcome
	log.Info("closing open sessions")
	runner.CloseAll() // saves each session's VM as a snapshot; the work in progress is in the database
	<-sweepsDone      // no snapshot sweep is half done
	log.Info("stopped")
	return runErr
}

// routes sends /api/ and /healthz to the API and everything else to the
// web app (a placeholder page unless built with -tags webui).
func routes(apiHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/", webui.Handler(webui.Files()))
	return mux
}

// tokenWarnBefore is how soon before a GitHub token expires serve warns.
const tokenWarnBefore = 3 * 24 * time.Hour

// githubTokens returns where publishing gets GitHub tokens from (the
// GITHUB_TOKEN personal access token, value) and the account it acts as,
// checked with GitHub now. Every task pushes a branch, so a missing token,
// or one GitHub rejects, stops startup.
func githubTokens(ctx context.Context, value string, gh *forge.GitHub, now time.Time, log *slog.Logger) (forge.TokenSource, string, error) {
	if value == "" {
		return nil, "", errors.New("GITHUB_TOKEN is not set: forgerun pushes every task's changes to a branch, so it needs a token that can push to your repos")
	}
	tok := forge.NewToken(value)
	id, err := gh.WhoAmI(ctx, tok)
	if err != nil {
		return nil, "", fmt.Errorf("checking GITHUB_TOKEN: %w", err)
	}
	expires := "never"
	if !id.Expires.IsZero() {
		expires = id.Expires.Format(time.DateOnly)
	}
	log.Info("github: authenticated", "as", id.Login, "token_expires", expires)
	if !id.Expires.IsZero() && id.Expires.Sub(now) < tokenWarnBefore {
		log.Warn("github token expires soon; pushes and pull requests will fail after that", "expires", id.Expires.Format(time.DateTime))
	}
	return forge.NewStaticToken(tok), id.Login, nil
}

// logAgents reports each agent forgerun can run and whether its credential
// is set. This is information only: a missing credential refuses tasks for
// that agent when they're created, and nothing else. An adapter without a
// row in the agents table is a setup error (migrations not applied).
func logAgents(ctx context.Context, db store.Store, agents agent.Set, log *slog.Logger) error {
	for _, id := range agents.IDs() {
		a, err := db.GetAgent(ctx, id)
		if errors.Is(err, store.ErrAgentNotFound) {
			return fmt.Errorf("agent %q has no row in the agents table; are the migrations applied?", id)
		}
		if err != nil {
			return err
		}
		ag, _ := agents.Get(id)
		credential := "set"
		if err := agents.Check(ctx, id); err != nil {
			credential = "missing (" + ag.Credential() + "); its tasks will be refused"
		}
		log.Info("agent", "id", id, "enabled", a.Enabled, "credential", credential)
	}
	return nil
}

// checkAgent makes sure the database knows the agent this server runs and
// that it is enabled, so a misconfiguration stops startup instead of failing
// every task.
func checkAgent(ctx context.Context, db store.Store, id string) error {
	a, err := db.GetAgent(ctx, id)
	if errors.Is(err, store.ErrAgentNotFound) {
		return fmt.Errorf("agent %q is not in the agents table; add it with a migration", id)
	}
	if err != nil {
		return err
	}
	if !a.Enabled {
		return fmt.Errorf("agent %q is disabled in the agents table", id)
	}
	return nil
}

// checkModels makes sure requests that name no model will resolve: the
// -model family (or the agent's own default) must exist among the agent's
// enabled models, after the ZDR filter if required. It returns the
// effective default model id and how many model families the agent offers.
func checkModels(ctx context.Context, db store.Store, agentID, defaultModel string, requireZDR bool) (string, int, error) {
	vs, err := db.ListModels(ctx)
	if err != nil {
		return "", 0, err
	}
	if requireZDR {
		vs = models.OnlyZDR(vs)
	}
	cat := models.NewCatalog(agentID, vs)
	v, err := cat.Resolve(models.Choice{Model: defaultModel}, "")
	if err != nil {
		if defaultModel != "" {
			return "", 0, fmt.Errorf("-model %q: %w", defaultModel, err)
		}
		return "", 0, fmt.Errorf("agent %q has no usable default model: %w", agentID, err)
	}
	return v.ID, len(cat.Families()), nil
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
