// Package task runs one coding task end to end in a fresh sandbox.
package task

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/sandbox"
)

const repoDir = "/work/repo"

// Task is what the user asked for.
type Task struct {
	ID      string
	Repo    string // git URL
	Prompt  string
	ModelID string // exact agent model id; "" or "auto" lets the agent choose
}

// New creates a task with a unique, sortable ID like "t-20261011-142233-a1b2c3".
func New(repo, prompt string) Task {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	id := fmt.Sprintf("t-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(b))
	return Task{ID: id, Repo: repo, Prompt: prompt}
}

// Result is what a task produced. Dir and LogPath are set even on failure,
// so you can always read the log.
type Result struct {
	Dir       string // out/<task-id>
	LogPath   string
	PatchPath string // "" if the agent changed nothing
}

// Runner holds settings shared by every task.
type Runner struct {
	Agent   agent.Agent
	OutDir  string
	CPUs    uint8
	MemMiB  uint32
	Timeout time.Duration
	Stdout  io.Writer // live progress; also saved to the log file
}

func (r *Runner) Run(ctx context.Context, t Task) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	res := Result{Dir: filepath.Join(r.OutDir, t.ID)}
	if err := os.MkdirAll(res.Dir, 0o755); err != nil {
		return res, err
	}
	res.LogPath = filepath.Join(res.Dir, "agent.log")
	logFile, err := os.Create(res.LogPath)
	if err != nil {
		return res, err
	}
	defer logFile.Close()
	logs := io.MultiWriter(r.Stdout, logFile)

	step := func(format string, a ...any) { fmt.Fprintf(logs, "==> "+format+"\n", a...) }

	step("creating sandbox %s (%s)", t.ID, r.Agent.Image())
	sb, err := sandbox.New(ctx, t.ID, sandbox.Config{
		Image:       r.Agent.Image(),
		CPUs:        r.CPUs,
		MemoryMiB:   r.MemMiB,
		MaxLifetime: r.Timeout + 2*time.Minute, // backstop if we crash
		Secrets:     r.Agent.Secrets(),
	})
	if err != nil {
		return res, err
	}
	defer func() {
		step("destroying sandbox")
		if err := sb.Destroy(); err != nil {
			fmt.Fprintf(logs, "warning: destroy %s: %v\n", sb.Name(), err)
		}
	}()

	step("cloning %s", t.Repo)
	if _, err := sb.Run(ctx, sandbox.Cmd{Name: "git", Args: []string{"clone", "--depth=1", t.Repo, repoDir}}); err != nil {
		return res, fmt.Errorf("clone: %w", err)
	}

	step("running %s agent", r.Agent.Name())
	if err := r.Agent.Run(ctx, sb, repoDir, t.Prompt, logs); err != nil {
		return res, fmt.Errorf("agent: %w", err)
	}

	step("collecting changes")
	git := func(args ...string) (string, error) {
		return sb.Run(ctx, sandbox.Cmd{Name: "git", Args: append([]string{"-C", repoDir}, args...)})
	}
	if _, err := git("add", "-A"); err != nil {
		return res, fmt.Errorf("git add: %w", err)
	}
	diff, err := git("diff", "--cached", "--binary")
	if err != nil {
		return res, fmt.Errorf("git diff: %w", err)
	}
	if diff == "" {
		step("agent made no changes")
		return res, nil
	}
	res.PatchPath = filepath.Join(res.Dir, "changes.patch")
	if err := os.WriteFile(res.PatchPath, []byte(diff), 0o644); err != nil {
		return res, fmt.Errorf("saving patch: %w", err)
	}
	step("patch saved to %s", res.PatchPath)
	return res, nil
}
