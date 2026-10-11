// Package sandbox runs commands inside an isolated microVM.
// It is the only package that talks to the microsandbox SDK.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// Config describes the VM to create.
type Config struct {
	Image     string
	CPUs      uint8
	MemoryMiB uint32
	// MaxLifetime is a VM-side kill switch: the runtime stops the VM after this
	// long even if our process crashed and never called Destroy.
	MaxLifetime time.Duration
}

// Cmd is a command to run in the sandbox. No shell is involved: Args are
// passed to the program exactly as given.
type Cmd struct {
	Name string
	Args []string
	Dir  string            // working directory inside the VM ("" = image default)
	Env  map[string]string // extra environment variables
}

func (c Cmd) String() string { return fmt.Sprintf("%s %v", c.Name, c.Args) }

// ExitError means the command ran but exited non-zero.
type ExitError struct {
	Cmd    Cmd
	Code   int
	Stderr string // tail of stderr, for the error message (empty when streamed)
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: exit code %d: %s", e.Cmd, e.Code, e.Stderr)
	}
	return fmt.Sprintf("%s: exit code %d", e.Cmd, e.Code)
}

// Sandbox is a running VM.
type Sandbox struct {
	vm *microsandbox.Sandbox
}

// EnsureRuntime installs the microsandbox runtime if missing. Call once at startup.
func EnsureRuntime(ctx context.Context) error {
	_, err := microsandbox.EnsureRuntime(ctx, microsandbox.RuntimeConfig{}, microsandbox.InstallOptions{})
	return err
}

// New boots a VM. The caller must call Destroy when done.
func New(ctx context.Context, name string, cfg Config) (*Sandbox, error) {
	vm, err := microsandbox.CreateSandbox(ctx, name,
		microsandbox.WithImage(cfg.Image),
		microsandbox.WithCPUs(cfg.CPUs),
		microsandbox.WithMemory(cfg.MemoryMiB),
		microsandbox.WithEphemeral(true),
		microsandbox.WithMaxDuration(cfg.MaxLifetime),
	)
	if err != nil {
		return nil, fmt.Errorf("creating sandbox %s: %w", name, err)
	}
	return &Sandbox{vm: vm}, nil
}

// Name returns the sandbox name.
func (s *Sandbox) Name() string { return s.vm.Name() }

// Run executes c, waits for it, and returns its stdout.
// A non-zero exit becomes an *ExitError.
func (s *Sandbox) Run(ctx context.Context, c Cmd) (string, error) {
	out, err := s.vm.Exec(ctx, c.Name, c.Args, c.execOptions()...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c, err) // transport failure: couldn't run it at all
	}
	if !out.Success() {
		return "", &ExitError{Cmd: c, Code: out.ExitCode(), Stderr: tail(out.Stderr(), 2000)}
	}
	return out.Stdout(), nil
}

// Stream executes c, copying stdout and stderr to w as they arrive.
// A non-zero exit becomes an *ExitError.
func (s *Sandbox) Stream(ctx context.Context, c Cmd, w io.Writer) error {
	h, err := s.vm.ExecStream(ctx, c.Name, c.Args, c.execOptions()...)
	if err != nil {
		return fmt.Errorf("%s: %w", c, err)
	}
	defer h.Close()

	exitCode, exited := 0, false
	for {
		ev, err := h.Recv(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
		switch ev.Kind {
		case microsandbox.ExecEventStdout, microsandbox.ExecEventStderr:
			if _, err := w.Write(ev.Data); err != nil {
				return fmt.Errorf("writing output of %s: %w", c, err)
			}
		case microsandbox.ExecEventFailed:
			msg := "unknown error"
			if ev.Failure != nil {
				msg = ev.Failure.Message
			}
			return fmt.Errorf("%s: failed to start: %s", c, msg)
		case microsandbox.ExecEventExited:
			exitCode, exited = ev.ExitCode, true
		case microsandbox.ExecEventDone:
			if !exited {
				return fmt.Errorf("%s: stream ended without exit status", c)
			}
			if exitCode != 0 {
				return &ExitError{Cmd: c, Code: exitCode}
			}
			return nil
		}
	}
}

// Destroy stops and removes the VM. It deliberately ignores the caller's
// context: cleanup must still happen after a timeout or Ctrl-C.
func (s *Sandbox) Destroy() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := s.vm.Destroy(ctx)
	if microsandbox.IsKind(err, microsandbox.ErrSandboxNotFound) {
		return nil // already removed (ephemeral cleanup won the race)
	}
	return err
}

func (c Cmd) execOptions() []microsandbox.ExecOption {
	var opts []microsandbox.ExecOption
	if c.Dir != "" {
		opts = append(opts, microsandbox.WithExecCwd(c.Dir))
	}
	if len(c.Env) > 0 {
		opts = append(opts, microsandbox.WithExecEnv(c.Env))
	}
	return opts
}

// tail keeps the last n bytes of s — enough of stderr to explain a failure
// without dumping megabytes into an error message.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
