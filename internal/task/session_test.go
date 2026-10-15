package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// clock is a fake time source for the session bookkeeping.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newTestSessions(idle time.Duration, max int) (*sessions, *clock) {
	c := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	s := newSessions(idle, max)
	s.now = c.now
	return s, c
}

func TestSessionsIdleTimeout(t *testing.T) {
	s, c := newTestSessions(30*time.Minute, 0)
	a, _ := s.reserve("a", "cursor")
	s.release(a)
	if info := s.info("a"); !info.Open || !info.IdleUntil.Equal(c.now().Add(30*time.Minute)) {
		t.Errorf("info = %+v", info)
	}
	c.add(29 * time.Minute)
	if got := s.expired(); len(got) != 0 {
		t.Errorf("expired early: %v", got)
	}
	// In use: never expires, however long it runs.
	ss, _ := s.take(context.Background(), "a")
	c.add(time.Hour)
	if got := s.expired(); len(got) != 0 {
		t.Errorf("a busy session expired: %v", got)
	}
	s.release(ss)
	c.add(31 * time.Minute)
	if got := s.expired(); len(got) != 1 || got[0].thread != "a" {
		t.Errorf("expired = %v, want a", got)
	}
	if s.info("a").Open {
		t.Error("an expired session is still listed")
	}
}

func TestSessionsCapEvictsLeastRecentlyUsed(t *testing.T) {
	s, c := newTestSessions(0, 2)
	a, _ := s.reserve("a", "cursor")
	s.release(a)
	c.add(time.Minute)
	b, _ := s.reserve("b", "cursor")
	s.release(b)
	c.add(time.Minute)
	if ss, _ := s.take(context.Background(), "a"); ss != nil { // a is used again: b is now the oldest
		s.release(ss)
	}
	_, evicted := s.reserve("c", "cursor")
	if len(evicted) != 1 || evicted[0].thread != "b" {
		t.Fatalf("evicted %v, want b", evicted)
	}
	// Busy sessions are never evicted, even over the limit.
	ss, _ := s.take(context.Background(), "a")
	_, evicted = s.reserve("d", "cursor")
	if len(evicted) != 0 || s.count() != 3 {
		t.Errorf("evicted %v with every session busy; %d open", evicted, s.count())
	}
	s.release(ss)
}

func TestSessionsTakeWaitsForBusy(t *testing.T) {
	s, _ := newTestSessions(0, 0)
	a, _ := s.reserve("a", "cursor") // busy
	got := make(chan *session)
	go func() {
		ss, _ := s.take(context.Background(), "a")
		got <- ss
	}()
	select {
	case <-got:
		t.Fatal("take returned while the session was busy")
	case <-time.After(50 * time.Millisecond):
	}
	s.release(a)
	if ss := <-got; ss != a || !ss.busy {
		t.Errorf("took %+v", ss)
	}

	// A caller that stops waiting gets ErrSessionBusy.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.take(ctx, "a"); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("take with a deadline: %v", err)
	}
	if ss, err := s.take(context.Background(), "none"); ss != nil || err != nil {
		t.Errorf("take of a thread without a session: %v, %v", ss, err)
	}
}

func TestSessionsRemove(t *testing.T) {
	s, _ := newTestSessions(0, 0)
	if _, err := s.remove("a"); !errors.Is(err, ErrNoSession) {
		t.Errorf("no session: %v", err)
	}
	a, _ := s.reserve("a", "cursor")
	if _, err := s.remove("a"); !errors.Is(err, ErrSessionBusy) {
		t.Errorf("busy: %v", err)
	}
	s.release(a)
	if ss, err := s.remove("a"); ss != a || err != nil {
		t.Errorf("remove = %v, %v", ss, err)
	}
	b, _ := s.reserve("b", "cursor")
	s.release(b)
	if all := s.removeAll(); len(all) != 1 || s.count() != 0 {
		t.Errorf("removeAll = %v, %d left", all, s.count())
	}
}

func TestReplayPrompt(t *testing.T) {
	got := replayPrompt([]Turn{
		{Prompt: "add a palindrome function", Reply: "Added is_palindrome to script.sh."},
		{Prompt: "make it strict", Failed: true},
	}, "now add tests")
	for _, want := range []string{
		"--- Message 1 from the user:\nadd a palindrome function",
		"--- Your reply:\nAdded is_palindrome to script.sh.",
		"--- Message 2 from the user:\nmake it strict\n\n--- (That turn failed; its changes were discarded.)",
		"one commit per turn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "--- New message from the user:\nnow add tests") {
		t.Errorf("the new message must come last:\n%s", got)
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 5}
	b.Write([]byte("hello "))
	b.Write([]byte("world"))
	if got := b.String(); got != "world" {
		t.Errorf("got %q", got)
	}
}
