package capacity

import (
	"strings"
	"testing"
)

func TestFree(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want Resources
	}{
		{"nothing open", Report{Total: Resources{8, 16384}, Reserved: Resources{1, 1024}}, Resources{7, 15360}},
		{"two VMs open", Report{Total: Resources{8, 16384}, Reserved: Resources{1, 1024}, Promised: Resources{4, 4096}}, Resources{3, 11264}},
		{"reserve bigger than the machine", Report{Total: Resources{1, 512}, Reserved: Resources{2, 1024}}, Resources{0, 0}},
		{"more promised than there is", Report{Total: Resources{4, 4096}, Promised: Resources{6, 8192}}, Resources{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Free(); got != tt.want {
				t.Errorf("Free() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFits(t *testing.T) {
	free := Resources{CPUs: 4, MemMiB: 4096}
	if !free.Fits(Resources{4, 4096}) {
		t.Error("an exact fit should fit")
	}
	if free.Fits(Resources{2, 8192}) || free.Fits(Resources{8, 1024}) {
		t.Error("too much memory or too many CPUs shouldn't fit")
	}
}

func TestParseMeminfo(t *testing.T) {
	got, err := parseMeminfo(strings.NewReader("MemFree:  1000 kB\nMemTotal:       16314660 kB\nSwapTotal: 0 kB\n"))
	if err != nil || got != 15932 {
		t.Fatalf("parseMeminfo = %d, %v; want 15932", got, err)
	}
	if _, err := parseMeminfo(strings.NewReader("MemFree: 1000 kB\n")); err == nil {
		t.Error("want an error without a MemTotal line")
	}
}

func TestParseBytes(t *testing.T) {
	got, err := parseBytes("17179869184\n")
	if err != nil || got != 16384 {
		t.Fatalf("parseBytes = %d, %v; want 16384", got, err)
	}
}

func TestMachine(t *testing.T) {
	m, err := Machine()
	if err != nil {
		t.Skip(err) // an OS without support
	}
	if m.CPUs < 1 || m.MemMiB < 256 {
		t.Errorf("Machine() = %+v, which can't be right", m)
	}
}
