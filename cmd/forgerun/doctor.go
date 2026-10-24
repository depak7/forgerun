package main

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/depak7/forgerun/internal/agent"
	"github.com/depak7/forgerun/internal/doctor"
	"github.com/depak7/forgerun/internal/forge"
	"github.com/depak7/forgerun/internal/remote"
	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store/postgres"
)

// errNotReady makes main exit 1 without printing anything more: the
// report has already said what's wrong.
var errNotReady = errors.New("setup problems found")

// doctorVMPrefix names the VM doctor boots, apart from serve's and workers'.
const doctorVMPrefix = "fd-"

func doctorCmd(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	role := fs.String("role", "", "what this machine is for: worker, serve or server (default: worker if FORGERUN_SERVER is set, else serve)")
	envFile := fs.String("env", "", "load settings from this env file first, e.g. ~/.forgerun-worker.env")
	quick := fs.Bool("quick", false, "don't boot a microVM to time a cold start")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Parse(args)

	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			return err
		}
	}
	r := doctor.Role(*role)
	if r == "" {
		r = doctor.RoleServe
		if os.Getenv("FORGERUN_SERVER") != "" {
			r = doctor.RoleWorker
		}
	}
	if r != doctor.RoleWorker && r != doctor.RoleServe && r != doctor.RoleServer {
		return fmt.Errorf("-role must be worker, serve or server, not %q", r)
	}

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stop()
	env := realEnv()
	if *quick {
		env.BootVM = nil
	}
	results := doctor.Run(ctx, r, env)
	if *asJSON {
		if err := doctor.PrintJSON(os.Stdout, r, results); err != nil {
			return err
		}
	} else {
		doctor.Print(os.Stdout, r, results)
	}
	if doctor.Failed(results) {
		return errNotReady
	}
	return nil
}

// realEnv is doctor's view of this machine.
func realEnv() doctor.Env {
	home, _ := os.UserHomeDir()
	var agents []doctor.Agent
	for _, a := range agent.Builtin() {
		agents = append(agents, doctor.Agent{ID: a.ID(), Credential: a.Credential()})
	}
	web := &http.Client{Timeout: 10 * time.Second}
	return doctor.Env{
		GOOS: goruntime.GOOS, GOARCH: goruntime.GOARCH, Home: home,
		Getenv:   os.Getenv,
		ReadFile: readHead,
		Stat:     os.Stat,
		OpenRW: func(path string) error {
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err == nil {
				f.Close()
			}
			return err
		},
		LookPath: exec.LookPath,
		Sysctl: func(name string) (string, error) {
			out, err := exec.Command("sysctl", "-n", name).Output()
			return string(out), err
		},
		FreeDisk: func(path string) (uint64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(path, &st); err != nil {
				return 0, err
			}
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		},
		Memory: memoryTotal,
		Reach: func(ctx context.Context, url string) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := web.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return nil // any answer, even 401, means it's reachable
		},
		Runtime: sandbox.RuntimeInstalled,
		MSBHome: sandbox.Home,
		BootVM:  bootVM,
		Hello: func(ctx context.Context, server, token string) error {
			err := (&remote.Client{URL: server, Token: token}).Hello(ctx)
			switch {
			case errors.Is(err, remote.ErrTokenRejected):
				return doctor.ErrTokenRejected
			case errors.Is(err, remote.ErrOutdated):
				return doctor.ErrOutdated
			}
			return err
		},
		Migrations: func(ctx context.Context, dbURL string) (int, int, error) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return postgres.MigrationStatus(ctx, cmp.Or(dbURL, defaultDatabaseURL))
		},
		GitHub: func(ctx context.Context, token string) (string, time.Time, error) {
			id, err := (&forge.GitHub{}).WhoAmI(ctx, forge.NewToken(token))
			return id.Login, id.Expires, err
		},
		SignIn: func() error {
			_, err := signIn(os.Getenv, slog.New(slog.DiscardHandler))
			return err
		},
		Agents: agents,
		Now:    time.Now,
	}
}

// bootVM boots a small VM, runs one command in it and removes it: the time
// it takes is a cold start. It removes any VM an interrupted run left.
func bootVM(ctx context.Context) error {
	if err := sandbox.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("installing the microsandbox runtime: %w", err)
	}
	sandbox.RemoveLeftovers(ctx, doctorVMPrefix)
	suffix := make([]byte, 3)
	rand.Read(suffix)
	sb, err := sandbox.New(ctx, doctorVMPrefix+"doctor-"+hex.EncodeToString(suffix),
		sandbox.Config{Image: "alpine", CPUs: 1, MemoryMiB: 512, MaxLifetime: 5 * time.Minute})
	if err != nil {
		return err
	}
	defer sb.Destroy()
	out, err := sb.Run(ctx, sandbox.Cmd{Name: "echo", Args: []string{"ok"}})
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("the VM answered %q", out)
	}
	return nil
}

// readHead reads up to 64 KB of a file: enough for /proc/cpuinfo's flags
// and a database header, without reading a whole database.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

// memoryTotal is the machine's RAM in bytes.
func memoryTotal() (uint64, error) {
	if goruntime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}
	b, err := readHead("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb * 1024, err
		}
	}
	return 0, errors.New("no MemTotal in /proc/meminfo")
}

// loadEnvFile sets the variables in an env file (KEY=value lines, as the
// installer writes and systemd reads), overriding the environment.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		os.Setenv(strings.TrimSpace(key), val)
	}
	return sc.Err()
}
