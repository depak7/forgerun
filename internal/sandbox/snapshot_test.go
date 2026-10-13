package sandbox

import (
	"context"
	"fmt"
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

// TestRollingSnapshot checks what rolling snapshots rely on: a snapshot of a
// restored VM outlives the snapshot it came from (deleting a parent works),
// compaction shortens the layer chain, and sizes are real, not virtual.
func TestRollingSnapshot(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot real VMs")
	}
	ctx := context.Background()
	cfg := Config{Image: "depak7/forgerun-agent-cursor:dev", CPUs: 1, MemoryMiB: 512, MaxLifetime: 5 * time.Minute}
	stamp := time.Now().Format("150405")
	run := func(sb *Sandbox, script string) string {
		t.Helper()
		out, err := sb.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", script}})
		if err != nil {
			t.Fatalf("%s: %v", sb.Name(), err)
		}
		return strings.TrimSpace(out)
	}
	snapshot := func(sb *Sandbox, name string) Snapshot {
		t.Helper()
		snap, err := sb.TakeSnapshot(ctx, name+"-"+stamp)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { DeleteSnapshot(context.Background(), snap.Ref) })
		if snap.SizeBytes <= 0 || snap.SizeBytes >= 1<<30 {
			t.Errorf("%s: size %d bytes, want real usage (>0, well under the 4 GiB virtual size)", name, snap.SizeBytes)
		}
		t.Logf("%s: %d KiB", name, snap.SizeBytes/1024)
		return snap
	}
	restore := func(ref, name string) *Sandbox {
		t.Helper()
		sb, err := Restore(ctx, ref, name+"-"+stamp, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sb.Destroy() })
		return sb
	}

	// Generation 1 from a fresh VM, then two more, each from a restore of the last.
	vm, err := New(ctx, "rolltest-0-"+stamp, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vm.Destroy() })
	run(vm, "echo v1 > /root/gen")
	prev := snapshot(vm, "rolltest-1")

	var compacted bool
	for gen := 2; gen <= 3; gen++ {
		vm := restore(prev.Ref, fmt.Sprintf("rolltest-r%d", gen))
		run(vm, fmt.Sprintf("echo v%d >> /root/gen", gen))
		if gen == 3 {
			before, after, err := vm.Compact(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("compacted %d -> %d layers", before, after)
			if after >= before {
				t.Errorf("compaction kept %d layers (was %d)", after, before)
			}
			compacted = true
		}
		next := snapshot(vm, fmt.Sprintf("rolltest-%d", gen))
		// The parent has a child now; deleting it must still work.
		if err := DeleteSnapshot(ctx, prev.Ref); err != nil {
			t.Fatalf("deleting generation %d: %v", gen-1, err)
		}
		prev = next
	}
	if !compacted {
		t.Fatal("compaction step never ran")
	}

	// The last snapshot survived its parents' deletion with every generation's data.
	last := restore(prev.Ref, "rolltest-final")
	if got := run(last, "cat /root/gen | tr '\\n' ' '"); got != "v1 v2 v3" {
		t.Errorf("final snapshot holds %q, want \"v1 v2 v3\"", got)
	}
}
