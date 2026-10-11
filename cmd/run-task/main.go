package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

const image = "depak7/forgerun-agent-cursor:dev"

func main() {
	repo := flag.String("repo", "", "git URL of the repo to work on")
	prompt := flag.String("prompt", "", "task for the agent")
	flag.Parse()

	if *repo == "" || *prompt == "" {
		log.Fatal("usage: run-task -repo <git-url> -prompt <task>")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := run(ctx, *repo, *prompt); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, repo, prompt string) error {
	key := os.Getenv("CURSOR_API_KEY")
	if key == "" {
		return fmt.Errorf("CURSOR_API_KEY is not set")
	}

	if _, err := microsandbox.EnsureRuntime(ctx, microsandbox.RuntimeConfig{}, microsandbox.InstallOptions{}); err != nil {
		return fmt.Errorf("installing runtime: %w", err)
	}

	name := fmt.Sprintf("task-%d", time.Now().Unix())
	fmt.Println("creating sandbox", name)
	sandbox, err := microsandbox.CreateSandbox(ctx, name,
		microsandbox.WithImage(image),
		microsandbox.WithCPUs(2),
		microsandbox.WithMemory(2048),
	)
	if err != nil {
		return fmt.Errorf("creating sandbox: %w", err)
	}
	defer sandbox.Stop(ctx)

	fmt.Println("cloning", repo)
	if _, err := sandbox.Exec(ctx, "git", []string{"clone", "--depth=1", repo, "/work/repo"}); err != nil {
		return fmt.Errorf("cloning: %w", err)
	}

	files, err := sandbox.Exec(ctx, "ls", []string{"-R", "/work/repo/src", "/work/repo/test"})
	if err != nil {
		return fmt.Errorf("listing files: %w", err)
	}
	fmt.Println(files.Stdout())

	fmt.Println("running agent...")
	script := `cd /work/repo && agent -p "$1" --force --trust --output-format text 2>&1; echo "agent exit code: $?"`
	agentOut, err := sandbox.Exec(ctx, "env", []string{"HOME=/root", "CURSOR_API_KEY=" + key, "sh", "-c", script, "sh", prompt})
	if err != nil {
		return fmt.Errorf("running agent: %w", err)
	}
	fmt.Println(agentOut.Stdout())

	diffOut, err := sandbox.Exec(ctx, "sh", []string{"-c", "cd /work/repo && git add -A && git diff --cached"})
	if err != nil {
		return fmt.Errorf("collecting diff: %w", err)
	}
	diff := diffOut.Stdout()

	if err := os.MkdirAll("out", 0o755); err != nil {
		return err
	}
	path := filepath.Join("out", name+".patch")
	if err := os.WriteFile(path, []byte(diff), 0o644); err != nil {
		return fmt.Errorf("saving patch: %w", err)
	}

	fmt.Println("patch saved to", path)
	return nil
}
