package task

import (
	"errors"
	"testing"
)

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to State
		ok       bool
	}{
		{Queued, Running, true},
		{Queued, Cancelled, true},
		{Running, Succeeded, true},
		{Running, Failed, true},
		{Running, Cancelled, true},

		{Queued, Succeeded, false}, // must run first
		{Running, Queued, false},   // no going back
		{Succeeded, Running, false},
		{Failed, Running, false}, // retries create a new task instead
		{Cancelled, Running, false},
	}
	for _, tc := range tests {
		err := tc.from.CheckTransition(tc.to)
		if tc.ok && err != nil {
			t.Errorf("%s → %s: unexpected error %v", tc.from, tc.to, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s → %s: want ErrInvalidTransition, got %v", tc.from, tc.to, err)
		}
	}
}

func TestTerminalAndValid(t *testing.T) {
	for _, s := range []State{Succeeded, Failed, Cancelled} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []State{Queued, Running} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	if State("bogus").Valid() {
		t.Error(`"bogus" should not be valid`)
	}
}
