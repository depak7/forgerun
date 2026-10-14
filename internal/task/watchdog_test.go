package task

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// watch runs a watchdog until it stops the "agent" or the test's deadline
// passes, while act simulates the agent. It returns the stop cause (nil if
// the agent was never stopped).
func watch(t *testing.T, d *watchdog, deadline time.Duration, act func(ctx context.Context)) error {
	t.Helper()
	ctx, stop := context.WithCancelCause(context.Background())
	defer stop(nil)
	go d.Watch(ctx, stop)
	actCtx, cancelAct := context.WithTimeout(ctx, deadline)
	defer cancelAct()
	act(actCtx)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
		return nil
	}
}

func TestWatchdogStopsSilentAgent(t *testing.T) {
	d := newWatchdog(100*time.Millisecond, nil)
	var log bytes.Buffer
	w := d.Writer(&log)
	start := time.Now()
	err := watch(t, d, 2*time.Second, func(ctx context.Context) {
		w.Write([]byte("working\nConnection lost, reconnecting (attempt 1)...\n"))
		<-ctx.Done() // then silence
	})
	if !errors.Is(err, errStalled) {
		t.Fatalf("cause = %v, want errStalled", err)
	}
	if !strings.Contains(err.Error(), `last output: "Connection lost, reconnecting (attempt 1)..."`) {
		t.Errorf("cause doesn't quote the last line: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("stopped after %s, want about 100ms", took)
	}
	if !strings.Contains(log.String(), "working") {
		t.Error("output didn't reach the log")
	}
}

func TestWatchdogOutputKeepsAgentAlive(t *testing.T) {
	d := newWatchdog(100*time.Millisecond, nil)
	w := d.Writer(&bytes.Buffer{})
	err := watch(t, d, 500*time.Millisecond, func(ctx context.Context) {
		for ctx.Err() == nil { // output every 20ms for 500ms: five times the limit
			w.Write([]byte("."))
			time.Sleep(20 * time.Millisecond)
		}
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an agent that keeps printing was stopped: %v", err)
	}
}

func TestWatchdogFileChangesKeepAgentAlive(t *testing.T) {
	var changed atomic.Bool // "the agent is editing files"
	changed.Store(true)
	d := newWatchdog(100*time.Millisecond, func(context.Context) bool { return changed.Load() })
	if err := watch(t, d, 400*time.Millisecond, func(ctx context.Context) { <-ctx.Done() }); err != nil {
		t.Errorf("an agent that keeps changing files was stopped: %v", err)
	}
	changed.Store(false) // now it stops editing too
	if err := watch(t, d, 2*time.Second, func(ctx context.Context) { <-ctx.Done() }); !errors.Is(err, errStalled) {
		t.Errorf("silent, idle agent: cause %v, want errStalled", err)
	}
}

func TestLastLine(t *testing.T) {
	tests := map[string]string{
		"one":                    "one",
		"a\nb\n\n":               "b",
		"  spaced  \n":           "spaced",
		strings.Repeat("x", 300): strings.Repeat("x", 200) + "…",
	}
	for in, want := range tests {
		if got := lastLine([]byte(in)); got != want {
			t.Errorf("lastLine(%.20q) = %q, want %q", in, got, want)
		}
	}
}
