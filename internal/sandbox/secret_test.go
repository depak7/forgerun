package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSecretNeverEntersVM(t *testing.T) {
	if os.Getenv("FORGERUN_INTEGRATION") == "" {
		t.Skip("set FORGERUN_INTEGRATION=1 to boot a real VM")
	}
	ctx := context.Background()
	const secret = "real-secret-value-123"

	sb, err := msb.New(ctx, "secret-test", Config{
		Image: "depak7/forgerun-agent-cursor:dev", CPUs: 1, MemoryMiB: 512, MaxLifetime: 5 * time.Minute,
		Secrets: []Secret{{EnvVar: "FAKE_KEY", Value: secret, AllowHosts: []string{"example.invalid"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()

	out, err := sb.Run(ctx, Cmd{Name: "printenv", Args: []string{"FAKE_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	inVM := strings.TrimSpace(out)
	if inVM == "" {
		t.Fatal("FAKE_KEY is not set in the VM at all")
	}
	if strings.Contains(inVM, secret) {
		t.Fatalf("real secret leaked into the VM: %q", inVM)
	}
	t.Logf("VM sees placeholder %q", inVM)
}
