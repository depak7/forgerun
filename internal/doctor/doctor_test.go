package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// healthy is a Linux worker with everything in place; tests break one thing.
func healthy() Env {
	env := map[string]string{
		"FORGERUN_SERVER": "https://forgerun.example.com", "FORGERUN_WORKER_TOKEN": "0123456789abcdef0123",
		"CURSOR_API_KEY": "k", "GITHUB_TOKEN": "t",
	}
	files := map[string][]byte{
		"/proc/cpuinfo":                   []byte("processor\t: 0\nflags\t\t: fpu vmx sse\n"),
		"/home/u/.microsandbox/db/msb.db": append([]byte("SQLite format 3\x00"), 0, 1),
		"/dev/kvm":                        nil,
	}
	return Env{
		GOOS: "linux", GOARCH: "amd64", Home: "/home/u",
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(p string) ([]byte, error) {
			if b, ok := files[p]; ok {
				return b, nil
			}
			return nil, fs.ErrNotExist
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if _, ok := files[p]; ok {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		OpenRW:     func(string) error { return nil },
		LookPath:   func(n string) (string, error) { return "/usr/bin/" + n, nil },
		FreeDisk:   func(string) (uint64, error) { return 100 * gb, nil },
		Memory:     func() (uint64, error) { return 16 * gb, nil },
		Reach:      func(context.Context, string) error { return nil },
		Runtime:    func() (string, error) { return "/home/u/.microsandbox/bin/msb", nil },
		MSBHome:    func() (string, error) { return "/home/u/.microsandbox", nil },
		BootVM:     func(context.Context) error { return nil },
		Hello:      func(context.Context, string, string) error { return nil },
		Migrations: func(context.Context, string) (int, int, error) { return 13, 13, nil },
		GitHub: func(context.Context, string) (string, time.Time, error) {
			return "alice", now.Add(60 * 24 * time.Hour), nil
		},
		Agents: []Agent{{ID: "cursor", Credential: "CURSOR_API_KEY"}, {ID: "opencode", Credential: "OPENCODE_API_KEY"}},
		Now:    func() time.Time { return now },
	}
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q check in %+v", name, rs)
	return Result{}
}

func names(rs []Result) string {
	var n []string
	for _, r := range rs {
		n = append(n, r.Name)
	}
	return strings.Join(n, ", ")
}

func TestHealthyWorker(t *testing.T) {
	rs := Run(context.Background(), RoleWorker, healthy())
	for _, r := range rs {
		if r.Status != Pass {
			t.Errorf("%s: %s %s", r.Name, r.Status, r.Detail)
		}
	}
	want := "platform, virtualization, microsandbox runtime, microsandbox database, disk, memory, git and tar, image registry, orchestrator, agent keys, cold start"
	if got := names(rs); got != want {
		t.Errorf("checks:\n got %s\nwant %s", got, want)
	}
}

// The orchestrator alone runs no VMs: no VM checks, but the database,
// GitHub token, sign-in and worker token.
func TestServerChecks(t *testing.T) {
	rs := Run(context.Background(), RoleServer, healthy())
	if got := names(rs); got != "git and tar, database, GITHUB_TOKEN, sign-in, worker token" {
		t.Errorf("checks: %s", got)
	}
	if Failed(rs) {
		t.Errorf("failed: %+v", rs)
	}
}

func TestVirtualization(t *testing.T) {
	tests := []struct {
		name    string
		cpuinfo string
		kvm     bool
		denied  bool
		status  Status
		detail  string
		fix     string
	}{
		{"usable", "flags : vmx", true, false, Pass, "/dev/kvm usable", ""},
		{"not in the kvm group", "flags : vmx", true, true, Fail, "can't use it", "usermod -aG kvm"},
		{"module not loaded", "flags : vmx", false, false, Fail, "/dev/kvm is missing", "modprobe kvm_intel"},
		// The Instabox case: a VM whose CPU shows virtualization, but no KVM.
		{"VM without KVM", "flags : vmx hypervisor", false, false, Fail, "this machine is a VM", "nested virtualization"},
		{"VM without nested virtualization", "flags : sse hypervisor", false, false, Fail, "without nested virtualization", "provider"},
		{"off in the BIOS", "flags : sse", false, false, Fail, "off or unsupported", "BIOS"},
	}
	for _, tc := range tests {
		e := healthy()
		e.ReadFile = func(p string) ([]byte, error) {
			if p == "/proc/cpuinfo" {
				return []byte(tc.cpuinfo), nil
			}
			return nil, fs.ErrNotExist
		}
		e.Stat = func(string) (fs.FileInfo, error) {
			if tc.kvm {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		}
		if tc.denied {
			e.OpenRW = func(string) error { return os.ErrPermission }
		}
		r := virtualization(e)
		if r.Status != tc.status || !strings.Contains(r.Detail, tc.detail) || !strings.Contains(r.Fix, tc.fix) {
			t.Errorf("%s: %s %q, fix %q", tc.name, r.Status, r.Detail, r.Fix)
		}
	}
}

func TestMac(t *testing.T) {
	e := healthy()
	e.GOOS, e.GOARCH = "darwin", "arm64"
	e.Sysctl = func(string) (string, error) { return "1\n", nil }
	if r := virtualization(e); r.Status != Pass {
		t.Errorf("Apple Silicon: %+v", r)
	}
	e.GOARCH = "amd64"
	if r := platform(e); r.Status != Fail || !strings.Contains(r.Detail, "Intel Mac") {
		t.Errorf("Intel Mac: %+v", r)
	}
}

// Booting a VM is pointless when the machine can't run one.
func TestColdStartSkippedWithoutVirtualization(t *testing.T) {
	e := healthy()
	e.Stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	booted := false
	e.BootVM = func(context.Context) error { booted = true; return nil }
	rs := Run(context.Background(), RoleWorker, e)
	if booted || !strings.Contains(find(t, rs, "cold start").Detail, "skipped") || !Failed(rs) {
		t.Errorf("booted=%v, %+v", booted, find(t, rs, "cold start"))
	}
}

func TestDamagedMSBDatabase(t *testing.T) {
	e := healthy()
	e.ReadFile = func(string) ([]byte, error) { return []byte("\x00\x00garbage"), nil }
	if r := msbDatabase(e); r.Status != Fail || !strings.Contains(r.Fix, "mv /home/u/.microsandbox/db/msb.db") {
		t.Errorf("%+v", r)
	}
	e.ReadFile = func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	if r := msbDatabase(e); r.Status != Pass {
		t.Errorf("no database yet: %+v", r)
	}
}

func TestDiskAndMemory(t *testing.T) {
	e := healthy()
	e.FreeDisk = func(string) (uint64, error) { return 3 * gb, nil }
	if r := disk(e); r.Status != Fail {
		t.Errorf("3 GB free: %+v", r)
	}
	e.FreeDisk = func(string) (uint64, error) { return 12 * gb, nil }
	if r := disk(e); r.Status != Warn {
		t.Errorf("12 GB free: %+v", r)
	}
	e.Memory = func() (uint64, error) { return 8 * gb, nil }
	if r := memory(e); r.Status != Warn || !strings.Contains(r.Fix, "-sessions 3") {
		t.Errorf("8 GB: %+v", r)
	}
	e.Memory = func() (uint64, error) { return 3 * gb, nil }
	if r := memory(e); r.Status != Fail {
		t.Errorf("3 GB: %+v", r)
	}
}

func TestOrchestrator(t *testing.T) {
	ctx := context.Background()
	e := healthy()
	e.Hello = func(context.Context, string, string) error { return ErrTokenRejected }
	if r := orchestrator(ctx, e); r.Status != Fail || !strings.Contains(r.Detail, "rejected") {
		t.Errorf("rejected: %+v", r)
	}
	e.Hello = func(context.Context, string, string) error {
		return errors.New("reaching the orchestrator: no such host")
	}
	if r := orchestrator(ctx, e); r.Status != Fail || !strings.Contains(r.Detail, "no such host") {
		t.Errorf("unreachable: %+v", r)
	}
	e = healthy()
	get := e.Getenv
	e.Getenv = func(k string) string {
		if k == "FORGERUN_SERVER" {
			return "http://10.0.0.5:8080"
		}
		return get(k)
	}
	if r := orchestrator(ctx, e); r.Status != Warn || !strings.Contains(r.Fix, "https") {
		t.Errorf("plain http: %+v", r)
	}
	e.Getenv = func(k string) string { return "" }
	if r := orchestrator(ctx, e); r.Status != Fail || !strings.Contains(r.Detail, "FORGERUN_SERVER") {
		t.Errorf("not set: %+v", r)
	}
}

func TestSignIn(t *testing.T) {
	env := map[string]string{}
	e := healthy()
	e.Getenv = func(k string) string { return env[k] }
	if r := signIn(e); r.Status != Pass || !strings.Contains(r.Detail, "local mode") {
		t.Errorf("local mode: %+v", r)
	}
	env["FORGERUN_PUBLIC_URL"] = "https://forgerun.example.com"
	if r := signIn(e); r.Status != Warn || !strings.Contains(r.Fix, "anyone who opens it is admin") {
		t.Errorf("local mode in public: %+v", r)
	}
	env["GITHUB_OAUTH_CLIENT_ID"] = "Iv23liABCDEF"
	if r := signIn(e); r.Status != Fail || !strings.Contains(r.Detail, "GitHub App") {
		t.Errorf("GitHub App: %+v", r)
	}
	env["GITHUB_OAUTH_CLIENT_ID"] = "Ov23liABCDEF"
	e.SignIn = func() error { return errors.New("FORGERUN_SECRET_KEY is not set") }
	if r := signIn(e); r.Status != Fail || !strings.Contains(r.Detail, "FORGERUN_SECRET_KEY") {
		t.Errorf("settings incomplete: %+v", r)
	}
	e.SignIn = func() error { return nil }
	if r := signIn(e); r.Status != Pass {
		t.Errorf("OAuth App: %+v", r)
	}
}

func TestGitHubToken(t *testing.T) {
	ctx := context.Background()
	e := healthy()
	e.GitHub = func(context.Context, string) (string, time.Time, error) {
		return "alice", now.Add(5 * 24 * time.Hour), nil
	}
	if r := githubToken(ctx, e); r.Status != Warn || !strings.Contains(r.Fix, "5 days") {
		t.Errorf("expiring: %+v", r)
	}
	e.GitHub = func(context.Context, string) (string, time.Time, error) {
		return "", time.Time{}, errors.New("Bad credentials")
	}
	if r := githubToken(ctx, e); r.Status != Fail || !strings.Contains(r.Detail, "Bad credentials") {
		t.Errorf("dead: %+v", r)
	}
}

func TestKeys(t *testing.T) {
	e := healthy()
	if r := keys(e); r.Status != Pass || r.Detail != "set: cursor; not set: opencode (OPENCODE_API_KEY)" {
		t.Errorf("%+v", r)
	}
	e.Getenv = func(string) string { return "" }
	if r := keys(e); r.Status != Warn || !strings.Contains(r.Fix, "need no key") {
		t.Errorf("none: %+v", r)
	}
}

func TestReport(t *testing.T) {
	var b strings.Builder
	rs := []Result{{Name: "virtualization", Status: Fail, Detail: "off", Fix: "turn it on"}, {Name: "disk", Status: Pass, Detail: "100 GB"}}
	Print(&b, RoleWorker, rs)
	out := b.String()
	for _, want := range []string{"forgerun doctor (worker)", "FAIL  virtualization", "→ turn it on", "1 problem(s) to fix"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestOrchestratorOutdated(t *testing.T) {
	e := healthy()
	e.Hello = func(context.Context, string, string) error { return ErrOutdated }
	if r := orchestrator(context.Background(), e); r.Status != Fail || !strings.Contains(r.Fix, "redeploy") {
		t.Errorf("%+v", r)
	}
}
