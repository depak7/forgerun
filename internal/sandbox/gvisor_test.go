package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDocker is a docker command that records each call (one per line) and
// answers the few queries start makes: the network's gateway is 127.0.0.1,
// so the key proxy listens there.
func fakeDocker(t *testing.T) (*GVisor, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$1 $2" in
"network inspect") echo 127.0.0.1 ;;
"info --format") echo '{"runc":{},"forgerun-runsc":{}}' ;;
esac
exit 0
`
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := NewGVisor(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	g.Docker = docker
	return g, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The real key is in no docker command: the sandbox gets a placeholder, and
// the key stays with the proxy.
func TestGVisorKeysNeverReachDocker(t *testing.T) {
	g, log := fakeDocker(t)
	sb, err := g.New(context.Background(), "fw-t1", Config{
		Image: "depak7/forgerun-agent-claude:dev", CPUs: 2, MemoryMiB: 2048, MaxLifetime: 12 * time.Hour,
		Secrets: []Secret{{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "sk-real-token", AllowHosts: []string{"api.anthropic.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()
	all := strings.Join(calls(t, log), "\n")
	if strings.Contains(all, "sk-real-token") {
		t.Fatalf("the real key reached docker:\n%s", all)
	}
	var run string
	for _, c := range calls(t, log) {
		if strings.HasPrefix(c, "run ") {
			run = c
		}
	}
	for _, want := range []string{
		"--runtime forgerun-runsc", "--network forgerun-net-fw-t1", "--cpus 2", "--memory 2048m",
		"-e CLAUDE_CODE_OAUTH_TOKEN=forgerun-secret-", "-e HTTPS_PROXY=http://127.0.0.1:",
		"-e NODE_EXTRA_CA_CERTS=/etc/forgerun/ca.pem", ":/etc/forgerun/ca.pem:ro",
		"--entrypoint sleep depak7/forgerun-agent-claude:dev 43200",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("docker run lacks %q:\n%s", want, run)
		}
	}
}

// Destroy removes the container and its network, once.
func TestGVisorDestroy(t *testing.T) {
	g, log := fakeDocker(t)
	sb, err := g.New(context.Background(), "fw-t2", Config{Image: "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	sb.Destroy()
	sb.Destroy()
	var rm, netRm int
	for _, c := range calls(t, log) {
		switch {
		case c == "rm -f fw-t2":
			rm++
		case c == "network rm forgerun-net-fw-t2":
			netRm++
		}
	}
	if rm != 1 || netRm != 1 {
		t.Errorf("rm %d, network rm %d; want 1 each", rm, netRm)
	}
}

func TestSnapshotRef(t *testing.T) {
	if got := snapshotRef("forgerunw-GitHub.com_x/Y", "t-2026"); got != "forgerun-snapshot:forgerunw-github.com_x-y-t-2026" {
		t.Errorf("snapshotRef = %q", got)
	}
	long := snapshotRef(strings.Repeat("a", 200), "n")
	if tag := strings.TrimPrefix(long, "forgerun-snapshot:"); len(tag) != 128 || !strings.HasSuffix(tag, "-n") {
		t.Errorf("long tag = %q (%d)", tag, len(tag))
	}
}

// TestGVisorReal boots real gVisor sandboxes. It needs Docker with the
// runsc runtime: set FORGERUN_GVISOR=1 (and FORGERUN_GVISOR_FLAGS, e.g.
// --ignore-cgroups, only when nested in a container).
func TestGVisorReal(t *testing.T) {
	if os.Getenv("FORGERUN_GVISOR") == "" {
		t.Skip("set FORGERUN_GVISOR=1 to boot real gVisor sandboxes")
	}
	ctx := context.Background()
	g, err := NewGVisor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := g.EnsureRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Image: "alpine", CPUs: 1, MemoryMiB: 512, MaxLifetime: 10 * time.Minute,
		Secrets: []Secret{{EnvVar: "TEST_KEY", Value: "the-real-value", AllowHosts: []string{"example.com"}}}}
	sb, err := g.New(ctx, "fgtest-a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()

	// gVisor's kernel, and only the placeholder.
	out, err := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "uname -r; echo $TEST_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gvisor") || !strings.Contains(out, "forgerun-secret-") || strings.Contains(out, "the-real-value") {
		t.Fatalf("inside: %q", out)
	}
	// Exit codes, streaming and files.
	var exit *ExitError
	if _, err := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo oops >&2; exit 3"}}); !errors.As(err, &exit) || exit.Code != 3 || exit.Stderr != "oops" {
		t.Errorf("exit: %v", err)
	}
	var b strings.Builder
	if err := sb.Stream(ctx, Cmd{Name: "sh", Args: []string{"-c", "echo one; echo two >&2"}}, &b); err != nil || !strings.Contains(b.String(), "one") || !strings.Contains(b.String(), "two") {
		t.Errorf("stream: %q, %v", b.String(), err)
	}
	if err := sb.WriteFile(ctx, "/work/repo/note.txt", []byte("kept")); err != nil {
		t.Fatal(err)
	}

	// Cancelling stops the command and everything it started inside the
	// sandbox, not just the docker client.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	start := time.Now()
	_, err = sb.Run(cctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 301 & sleep 302"}})
	cancel()
	if err == nil || time.Since(start) > 30*time.Second {
		t.Fatalf("cancel: %v after %s", err, time.Since(start))
	}
	// Exact command lines, so the check can't match itself.
	left, _ := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `n=0; for p in /proc/[0-9]*; do c=$(tr '\0' ' ' < $p/cmdline 2>/dev/null); case "$c" in "sleep 301 "|"sleep 302 ") n=$((n+1));; esac; done; echo $n`}})
	if strings.TrimSpace(left) != "0" {
		t.Errorf("after cancel, %s of the command's processes are still running", strings.TrimSpace(left))
	}

	// A snapshot keeps the files; a restore gets fresh placeholders.
	snap, err := sb.TakeSnapshot(ctx, "fgtest", "one")
	if err != nil {
		t.Fatal(err)
	}
	defer g.DeleteSnapshot(ctx, snap.Ref)
	r, err := g.Restore(ctx, snap.Ref, "fgtest-b", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Destroy()
	if out, err := r.Run(ctx, Cmd{Name: "cat", Args: []string{"/work/repo/note.txt"}}); err != nil || out != "kept" {
		t.Errorf("restored file: %q, %v", out, err)
	}
	infos, err := g.ListSnapshots(ctx)
	if err != nil || !containsRef(infos, snap.Ref) {
		t.Errorf("ListSnapshots: %+v, %v", infos, err)
	}

	// Leftovers with the prefix are removed.
	if n, err := g.RemoveLeftovers(ctx, "fgtest-"); err != nil || n != 2 {
		t.Errorf("RemoveLeftovers: %d, %v", n, err)
	}
	if !strings.Contains(snap.Ref, "fgtest") || g.SnapshotGone(snap.Ref) {
		t.Errorf("snapshot %s gone too early", snap.Ref)
	}
}

func containsRef(infos []SnapshotInfo, ref string) bool {
	for _, i := range infos {
		if i.Ref == ref {
			return true
		}
	}
	return false
}
