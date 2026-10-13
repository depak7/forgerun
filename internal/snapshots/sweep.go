package snapshots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/depak7/forgerun/internal/sandbox"
	"github.com/depak7/forgerun/internal/store"
)

// Limits bound how much disk snapshots may use. A zero field means no limit.
type Limits struct {
	MaxAge time.Duration // delete snapshots no task has used for longer than this
	Budget int64         // total real disk usage in bytes; least recently used go first
}

// orphanAge is how old an unrecorded snapshot must be before Sweep deletes
// it. A task that's saving right now has taken its snapshot but not yet
// recorded it; it must not be mistaken for an orphan.
const orphanAge = 10 * time.Minute

// Reason says why a snapshot is deleted.
type Reason string

const (
	ReasonRetired    Reason = "retired"     // replaced, but its delete didn't finish
	ReasonExpired    Reason = "expired"     // unused for longer than Limits.MaxAge
	ReasonOverBudget Reason = "over budget" // evicted to fit Limits.Budget
)

// Eviction is a snapshot to delete and why.
type Eviction struct {
	Snapshot store.Snapshot
	Reason   Reason
}

// plan decides which snapshots to delete. snaps must be least recently used
// first (as store.ListSnapshots returns them). It returns the deletions and
// how many snapshots and bytes remain. It does no I/O, so it's easy to test.
func plan(snaps []store.Snapshot, now time.Time, l Limits) (evict []Eviction, keptCount int, keptBytes int64) {
	var kept []store.Snapshot
	for _, s := range snaps {
		switch {
		case s.State == store.SnapshotRetired:
			evict = append(evict, Eviction{s, ReasonRetired})
		case l.MaxAge > 0 && now.Sub(s.LastUsedAt) > l.MaxAge:
			evict = append(evict, Eviction{s, ReasonExpired})
		default:
			kept = append(kept, s)
			keptBytes += s.SizeBytes
		}
	}
	// kept is still least recently used first, so evict from the front.
	for l.Budget > 0 && keptBytes > l.Budget && len(kept) > 0 {
		evict = append(evict, Eviction{kept[0], ReasonOverBudget})
		keptBytes -= kept[0].SizeBytes
		kept = kept[1:]
	}
	return evict, len(kept), keptBytes
}

// SweepReport counts what a sweep did.
type SweepReport struct {
	Deleted   map[Reason]int // snapshots deleted, by reason
	Orphans   int            // on disk but not in the store
	Missing   int            // in the store but gone from disk
	Kept      int
	KeptBytes int64
}

func (r SweepReport) String() string {
	n := r.Orphans
	for _, c := range r.Deleted {
		n += c
	}
	return fmt.Sprintf("deleted %d (%d retired, %d orphans, %d expired, %d over budget), forgot %d missing; %d snapshots, %s",
		n, r.Deleted[ReasonRetired], r.Orphans, r.Deleted[ReasonExpired], r.Deleted[ReasonOverBudget],
		r.Missing, r.Kept, size(r.KeptBytes))
}

// Sweep brings snapshots back within bounds: it finishes interrupted
// deletes, removes orphans and expired snapshots, and evicts least recently
// used ones while over budget. now is passed in so tests can control time.
// It keeps going after a failure and returns all the errors together.
func (m *Manager) Sweep(ctx context.Context, now time.Time) (SweepReport, error) {
	rep := SweepReport{Deleted: map[Reason]int{}}
	snaps, err := m.Store.ListSnapshots(ctx)
	if err != nil {
		return rep, err
	}
	var errs []error

	// Rows whose files are gone (e.g. removed by hand) just get marked.
	var live []store.Snapshot
	for _, s := range snaps {
		if !sandbox.SnapshotGone(s.Ref) {
			live = append(live, s)
			continue
		}
		if err := m.Store.MarkSnapshotDeleted(ctx, s.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Missing++
	}

	// Orphans: in one of our groups on disk, but no row knows them.
	known := make(map[string]bool, len(live))
	for _, s := range live {
		known[s.Ref] = true
	}
	onDisk, err := sandbox.ListSnapshots(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, info := range onDisk {
		if known[info.Ref] || !strings.HasPrefix(info.Group, m.prefix()+"-") || now.Sub(info.CreatedAt) < orphanAge {
			continue
		}
		if err := sandbox.DeleteSnapshot(ctx, info.Ref); err != nil {
			errs = append(errs, err)
			continue
		}
		rep.Orphans++
	}

	evict, kept, keptBytes := plan(live, now, m.Limits)
	rep.Kept, rep.KeptBytes = kept, keptBytes
	for _, e := range evict {
		if err := m.remove(ctx, e.Snapshot); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s snapshot %d: %w", e.Reason, e.Snapshot.ID, err))
			rep.Kept++ // still there
			rep.KeptBytes += e.Snapshot.SizeBytes
			continue
		}
		rep.Deleted[e.Reason]++
	}
	return rep, errors.Join(errs...)
}

// RunSweeps sweeps every interval until ctx is cancelled. Call SweepAndLog
// once first, at startup, if a sweep should happen right away.
func (m *Manager) RunSweeps(ctx context.Context, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.SweepAndLog(ctx, log)
		}
	}
}

// SweepAndLog runs one sweep and logs what it did.
func (m *Manager) SweepAndLog(ctx context.Context, log *slog.Logger) {
	rep, err := m.Sweep(ctx, time.Now())
	if err != nil {
		log.Error("snapshot sweep", "result", rep.String(), "err", err)
		return
	}
	log.Info("snapshot sweep", "result", rep.String())
}
