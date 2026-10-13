package store

import "testing"

func TestNormalizeRepo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/expressjs/express", "https://github.com/expressjs/express"},
		{"https://github.com/expressjs/express.git", "https://github.com/expressjs/express"},
		{"https://github.com/expressjs/express/", "https://github.com/expressjs/express"},
		{"  HTTPS://GitHub.com/expressjs/express.git/ ", "https://github.com/expressjs/express"},
		{"https://github.com/ExpressJS/Express", "https://github.com/ExpressJS/Express"}, // path case kept
	}
	for _, tc := range tests {
		if got := NormalizeRepo(tc.in); got != tc.want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
