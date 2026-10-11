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
	switch os.Args[1] {
	case "run":
		if err := runCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			var exitErr *sandbox.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(2) // a command inside the sandbox failed
			}
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: forgerun run -repo <git-url> -prompt <task> [-timeout 15m] [-out out]")
	os.Exit(64)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", "", "git URL of the repo to work on")
	prompt := fs.String("prompt", "", "task for the agent")
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
