// Command forgerun runs coding agents inside isolated microVMs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
		err = serveCmd(os.Args[2:])
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
                 [-snapshot-max-age 168h] [-snapshot-budget 20GB] [-snapshot-sweep 10m]
  forgerun run   -repo <git-url> -prompt <task> [-timeout 15m] [-out out]

The database URL defaults to $FORGERUN_DATABASE_URL, then the docker-compose dev database.
serve reads CURSOR_API_KEY, and GITHUB_TOKEN (optional) to open pull requests.
`)
	os.Exit(64)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", "", "git URL of the repo to work on")
	prompt := fs.String("prompt", "", "task for the agent")
	model := fs.String("model", "", "exact agent model id, e.g. claude-opus-5-5-high (default: the agent's own choice)")
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

	ag, err := agent.NewCursor(os.Getenv("CURSOR_API_KEY"))
	if err != nil {
		return err
	}
	if err := sandbox.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("microsandbox runtime: %w", err)
	}

	r := &task.Runner{Agent: ag, OutDir: *out, CPUs: 2, MemMiB: 2048, Timeout: *timeout, Stdout: os.Stdout}
	t := task.New(*repo, *prompt)
	t.ModelID = *model
	res, err := r.Run(ctx, t)
	if res.LogPath != "" {
		fmt.Println("log:", res.LogPath)
	}
	if err != nil {
		return err
	}
	if res.PatchPath == "" {
		fmt.Println("result: no changes")
	} else {
		fmt.Println("result:", res.PatchPath)
	}
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
