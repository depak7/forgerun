package task

import (
	"errors"
	"fmt"
	"slices"
)

// State is where a task is in its lifecycle. Values are stored in the
// database as-is, so never rename an existing one.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// transitions lists every allowed move. States with no entry are terminal.
var transitions = map[State][]State{
	Queued:  {Running, Cancelled},
	Running: {Succeeded, Failed, Cancelled},
}

// ErrInvalidTransition is returned when a state change isn't allowed.
var ErrInvalidTransition = errors.New("invalid state transition")

// Terminal reports whether s is a final state.
func (s State) Terminal() bool {
	return s == Succeeded || s == Failed || s == Cancelled
}

// Valid reports whether s is one of the known states.
func (s State) Valid() bool {
	return s == Queued || s == Running || s.Terminal()
}

// CheckTransition returns nil if a task may move from s to next.
func (s State) CheckTransition(next State) error {
	if !slices.Contains(transitions[s], next) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, s, next)
	}
	return nil
}
