// Command forgerun runs coding agents inside isolated microVMs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/task"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "serve":
		err = serveCmd(os.Args[2:], false)
	case "server":
		err = serveCmd(os.Args[2:], true)
	case "worker":
		err = workerCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var exitErr *sandbox.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(2) // a command inside the sandbox failed
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  forgerun serve [-addr 127.0.0.1:8080] [-workers 2] [-timeout 15m] [-agent-idle 10m] [-out out] [-db url]
                 [-model family] [-require-zdr] [-snapshots=false]
                 [-session-idle 30m] [-sessions 4]
                 [-snapshot-max-age 168h] [-snapshot-budget 20GB] [-snapshot-sweep 10m]
  forgerun server [the same flags as serve]   the orchestrator alone: API, web UI, database; no VMs here
  forgerun worker -server https://… [-name n] [-workers 2] [-sessions 4] [-snapshots=false]
                                            runs turns for a forgerun server; dials out, nothing to open
  forgerun run   -repo <git-url> -prompt <task> [-agent cursor] [-model m] [-effort e] [-timeout 15m] [-out out]

The database URL defaults to $FORGERUN_DATABASE_URL, then the docker-compose dev database.
Agents read their credential when a task needs it: CURSOR_API_KEY (cursor),
CLAUDE_CODE_OAUTH_TOKEN (claude-code, from claude setup-token). serve also
needs GITHUB_TOKEN to push threads' branches and open pull requests.
server and worker share FORGERUN_WORKER_TOKEN; agent keys belong on the workers.
`)
	os.Exit(64)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", "", "git URL of the repo to work on")
	prompt := fs.String("prompt", "", "task for the agent")
	agentID := fs.String("agent", "cursor", "agent to run: "+strings.Join(agent.NewSet(nil, agent.Builtin()...).IDs(), ", "))
	model := fs.String("model", "", "model: an exact id for cursor (e.g. claude-opus-5-5-high), an alias for claude-code (e.g. opus); default: the agent's own")
	effort := fs.String("effort", "", "effort, for agents that take it separately (claude-code: low … max)")
	timeout := fs.Duration("timeout", 15*time.Minute, "max time for the whole task")
	out := fs.String("out", "out", "directory for task results")
	fs.Parse(args)
	if *repo == "" || *prompt == "" {
		usage()
	}

	// Ctrl-C / SIGTERM cancel ctx instead of killing us, so the VM is destroyed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C now force-quits
	}()

	if err := sandbox.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("microsandbox runtime: %w", err)
	}

	// The agent's credential is read from its environment variable when the task starts.
	agents := agent.NewSet(agent.EnvCredentials{}, agent.Builtin()...)
	r := &task.Runner{Agents: agents, DefaultAgent: *agentID, OutDir: *out, CPUs: 2, MemMiB: 2048, Timeout: *timeout, Stdout: os.Stdout}
	t := task.New(*repo, *prompt)
	t.ModelID, t.Family, t.Effort = *model, *model, *effort
	// No models table here to say which models need the agent's key: run
	// without one if it's missing, and let the agent report it if needed.
	t.Keyless = true
	res, err := r.Run(ctx, t)
	r.CloseAll() // one turn only: destroy its session's VM
	if res.LogPath != "" {
		fmt.Println("log:", res.LogPath)
	}
	if err != nil {
		return err
	}
	if res.Diff == "" {
		fmt.Println("result: no changes")
		return nil
	}
	// No database here: the changes go to a file next to the log.
	path := filepath.Join(res.Dir, "changes.patch")
	if err := os.WriteFile(path, []byte(res.Diff), 0o644); err != nil {
		return err
	}
	fmt.Println("result:", path)
	return nil
}

const defaultDatabaseURL = "postgres://forgerun:forgerun@localhost:5433/forgerun?sslmode=disable"

// dbFlag registers -db on fs. Precedence: flag, then env, then default.
func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("FORGERUN_DATABASE_URL")
	if def == "" {
		def = defaultDatabaseURL
	}
	return fs.String("db", def, "Postgres URL (env FORGERUN_DATABASE_URL)")
}
