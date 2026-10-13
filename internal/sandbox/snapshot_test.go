package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSnapshotRestore repeats the snapshot experiment (docs/experiments.md)
// in Go: snapshot a VM, restore two independent VMs from it, attach a
// different secret to each, then delete the snapshot.
func TestSnapshotRestore(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot real VMs")
	}
	ctx := context.Background()
	cfg := Config{Image: "depak7/forgerun-agent-cursor:dev", CPUs: 1, MemoryMiB: 512, MaxLifetime: 5 * time.Minute}
	run := func(sb *Sandbox, script string) string {
		t.Helper()
		out, err := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", script}})
		if err != nil {
			t.Fatalf("%s: %v", sb.Name(), err)
		}
		return strings.TrimSpace(out)
	}

	// 1. A builder with no secrets prepares files (stand-ins for clone + install).
	builder, err := New(ctx, "snaptest-builder", cfg)
	if err != nil {
		t.Fatal(err)
	}
	run(builder, "mkdir -p /work/repo/node_modules/dep && echo v1 > /work/repo/node_modules/dep/version")

	// 2. Snapshot it while running, then the builder can go.
	snap, err := builder.TakeSnapshot(ctx, "snaptest-"+time.Now().Format("150405"))
	builder.Destroy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DeleteSnapshot(context.Background(), snap.Ref) })
	t.Logf("snapshot %s", snap.Ref)

	// 3. Two restores, each with its own secret.
	restore := func(name, secret string) *Sandbox {
		t.Helper()
		c := cfg
		c.Secrets = []Secret{{EnvVar: "FAKE_KEY", Value: secret, AllowHosts: []string{"example.invalid"}}}
		sb, err := Restore(ctx, snap.Ref, name, c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sb.Destroy() })
		return sb
	}
	r1 := restore("snaptest-r1", "real-secret-one")
	r2 := restore("snaptest-r2", "real-secret-two")

	// 4. Files survived, VMs are isolated, secrets are placeholders only.
	for _, sb := range []*Sandbox{r1, r2} {
		if got := run(sb, "cat /work/repo/node_modules/dep/version"); got != "v1" {
			t.Errorf("%s: prepared file = %q, want v1", sb.Name(), got)
		}
		env := run(sb, "printenv FAKE_KEY || true")
		if env == "" {
			t.Errorf("%s: secret was not attached", sb.Name())
		}
		if strings.Contains(env, "real-secret") {
			t.Errorf("%s: real secret visible inside the VM: %q", sb.Name(), env)
		}
	}
	run(r1, "echo only-in-r1 > /work/repo/r1-marker")
	if got := run(r2, "test -e /work/repo/r1-marker && echo shared || echo isolated"); got != "isolated" {
		t.Error("restored VMs share a disk; writes in one appear in the other")
	}

	// 5. After deletion, the snapshot can't be restored; deleting twice is fine.
	if err := DeleteSnapshot(ctx, snap.Ref); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSnapshot(ctx, snap.Ref); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if sb, err := Restore(ctx, snap.Ref, "snaptest-r3", cfg); err == nil {
		sb.Destroy()
		t.Error("restored from a deleted snapshot")
	}
}
