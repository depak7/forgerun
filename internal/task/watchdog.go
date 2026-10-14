package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// errStalled is the cancel cause when the watchdog stops a silent agent.
var errStalled = errors.New("agent stalled")

// watchdog stops an agent that has gone silent: no output and no file
// changes for longer than idle. Agents print nothing while waiting on a
// dropped connection, and without this a stuck task would hold its worker
// until the task timeout.
type watchdog struct {
	idle  time.Duration
	every time.Duration              // how often to check
	probe func(context.Context) bool // reports whether files changed since the last probe
	now   func() time.Time           // the clock; replaced in tests

	mu       sync.Mutex
	last     time.Time // last activity
	lastLine string    // the agent's most recent output, to explain a stall
}

// newWatchdog checks every quarter of idle, but at least every 30s.
func newWatchdog(idle time.Duration, probe func(context.Context) bool) *watchdog {
	return &watchdog{idle: idle, every: min(idle/4, 30*time.Second), probe: probe, now: time.Now}
}

// Writer wraps w so everything the agent writes counts as activity.
func (d *watchdog) Writer(w io.Writer) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		d.mu.Lock()
		d.last = d.now()
		if line := lastLine(p); line != "" {
			d.lastLine = line
		}
		d.mu.Unlock()
		return w.Write(p)
	})
}

// Watch cancels stop when the agent has been silent for too long, and
// returns when ctx ends. Run it in its own goroutine.
func (d *watchdog) Watch(ctx context.Context, stop context.CancelCauseFunc) {
	d.mu.Lock()
	d.last = d.now()
	d.mu.Unlock()
	ticker := time.NewTicker(d.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if d.probe != nil && d.probe(ctx) {
			d.mu.Lock()
			d.last = d.now()
			d.mu.Unlock()
		}
		d.mu.Lock()
		silent, line := d.now().Sub(d.last), d.lastLine
		d.mu.Unlock()
		if silent >= d.idle {
			stop(fmt.Errorf("%w: no output or file changes for %s (last output: %q)", errStalled, d.idle, line))
			return
		}
	}
}

// lastLine is the last non-empty line of p, shortened for an error message.
func lastLine(p []byte) string {
	lines := strings.Split(strings.TrimSpace(string(p)), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}

// writerFunc turns a function into an io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
