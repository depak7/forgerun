package task

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/depak7/forgerun/internal/sandbox"
)

// ErrSessionBusy means a thread's session is in use (a turn or a summary
// is running in it) for longer than a caller was willing to wait.
var ErrSessionBusy = errors.New("the session is busy")

// ErrNoSession means a thread has no open session.
var ErrNoSession = errors.New("no open session")

// session is a thread's VM, kept between turns so the agent keeps its
// conversation and the work in progress.
type session struct {
	thread  string
	agentID string
	repo    string           // the repo URL, which its snapshot is saved under
	sb      *sandbox.Sandbox // nil while it's being opened
	// continued: the agent has a conversation in this VM that the next
	// turn can carry on (see agent.Request.Continue).
	continued bool
	busy      bool
	used      time.Time // when it was last released
}

// sessions is the bookkeeping of open sessions: which thread has one,
// whether it's in use, and which have been idle too long. It never touches
// a VM itself; the Runner opens and closes them.
type sessions struct {
	idle time.Duration // close a session unused for this long; 0 = never
	max  int           // most sessions open at once; 0 = no limit
	now  func() time.Time

	mu      sync.Mutex
	changed *sync.Cond // signalled when a session is released or removed
	open    map[string]*session
}

func newSessions(idle time.Duration, max int) *sessions {
	s := &sessions{idle: idle, max: max, now: time.Now, open: map[string]*session{}}
	s.changed = sync.NewCond(&s.mu)
	return s
}

// take marks the thread's open session busy and returns it, waiting while
// another caller holds it. It returns nil if the thread has none.
func (s *sessions) take(ctx context.Context, thread string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { // wake the wait below when ctx ends
		s.mu.Lock()
		defer s.mu.Unlock()
		s.changed.Broadcast()
	})
	defer stop()
	for {
		ss := s.open[thread]
		if ss == nil {
			return nil, nil
		}
		if !ss.busy {
			ss.busy = true
			return ss, nil
		}
		if ctx.Err() != nil {
			return nil, ErrSessionBusy
		}
		s.changed.Wait()
	}
}

// reserve records a new, busy session for thread, to be opened by the
// caller. If that goes over the limit, the least recently used idle
// sessions are removed and returned for the caller to close.
func (s *sessions) reserve(thread, agentID string) (ss *session, evicted []*session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss = &session{thread: thread, agentID: agentID, busy: true}
	s.open[thread] = ss
	for s.max > 0 && len(s.open) > s.max {
		var lru *session
		for _, o := range s.open {
			if !o.busy && (lru == nil || o.used.Before(lru.used)) {
				lru = o
			}
		}
		if lru == nil {
			break // all busy: over the limit until one is released
		}
		delete(s.open, lru.thread)
		evicted = append(evicted, lru)
	}
	return ss, evicted
}

// release marks a session idle again.
func (s *sessions) release(ss *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss.busy, ss.used = false, s.now()
	s.changed.Broadcast()
}

// drop forgets a session its holder is closing (it broke, or its thread
// changed agent).
func (s *sessions) drop(ss *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open[ss.thread] == ss {
		delete(s.open, ss.thread)
	}
	s.changed.Broadcast()
}

// remove takes the thread's idle session out, for closing. It returns
// ErrSessionBusy if it's in use, ErrNoSession if there is none.
func (s *sessions) remove(thread string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.open[thread]
	switch {
	case ss == nil:
		return nil, ErrNoSession
	case ss.busy:
		return nil, ErrSessionBusy
	}
	delete(s.open, thread)
	return ss, nil
}

// expired takes out every idle session unused for longer than the idle
// limit, for closing.
func (s *sessions) expired() []*session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*session
	for thread, ss := range s.open {
		if s.idle > 0 && !ss.busy && s.now().Sub(ss.used) > s.idle {
			delete(s.open, thread)
			out = append(out, ss)
		}
	}
	return out
}

// removeAll takes out every idle session, for closing at shutdown.
func (s *sessions) removeAll() []*session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*session
	for thread, ss := range s.open {
		if !ss.busy {
			delete(s.open, thread)
			out = append(out, ss)
		}
	}
	return out
}

// SessionInfo is what's known about a thread's session.
type SessionInfo struct {
	Open      bool
	Busy      bool      // a turn or summary is running in it
	IdleUntil time.Time // when an idle session will be closed; zero if open-ended or busy
}

func (s *sessions) info(thread string) SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.open[thread]
	if ss == nil {
		return SessionInfo{}
	}
	info := SessionInfo{Open: true, Busy: ss.busy}
	if !ss.busy && s.idle > 0 {
		info.IdleUntil = ss.used.Add(s.idle)
	}
	return info
}

// list describes every open session.
func (s *sessions) list() []OpenSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OpenSession, 0, len(s.open))
	for thread, ss := range s.open {
		o := OpenSession{Thread: thread, Agent: ss.agentID, Busy: ss.busy}
		if !ss.busy && s.idle > 0 {
			o.IdleUntil = ss.used.Add(s.idle)
		}
		out = append(out, o)
	}
	return out
}

// OpenSession is one open session, as a worker reports it.
type OpenSession struct {
	Thread    string    `json:"thread"`
	Agent     string    `json:"agent"`
	Busy      bool      `json:"busy"`
	IdleUntil time.Time `json:"idle_until"` // zero if busy or open-ended
}

func (s *sessions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}
