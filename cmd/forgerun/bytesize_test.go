package main

import "testing"

func TestByteSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"20GB", 20e9}, {"500MB", 500e6}, {"1.5gb", 1.5e9}, {" 2 TB ", 2e12}, {"1024", 1024}, {"0", 0}, {"7B", 7},
	}
	for _, tc := range tests {
		var b byteSize
		if err := b.Set(tc.in); err != nil || int64(b) != tc.want {
			t.Errorf("Set(%q) = %d, %v; want %d", tc.in, b, err, tc.want)
		}
	}
	for _, bad := range []string{"", "lots", "-1GB", "GB", "5XB"} {
		var b byteSize
		if err := b.Set(bad); err == nil {
			t.Errorf("Set(%q) accepted %d", bad, b)
		}
	}
	for in, want := range map[byteSize]string{20e9: "20GB", 1.5e9: "1.5GB", 0: "0", 512: "512"} {
		if got := in.String(); got != want {
			t.Errorf("String(%d) = %q, want %q", in, got, want)
		}
	}
}
