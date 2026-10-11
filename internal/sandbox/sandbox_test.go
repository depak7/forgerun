package sandbox

import "testing"

func TestExitErrorMessage(t *testing.T) {
	err := &ExitError{Cmd: Cmd{Name: "git", Args: []string{"clone", "x"}}, Code: 128, Stderr: "repo not found"}
	want := "git [clone x]: exit code 128: repo not found"
	if got := err.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTail(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"  short  ", 10, "short"},
		{"abcdef", 3, "…def"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		if got := tail(tc.in, tc.n); got != tc.want {
			t.Errorf("tail(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
