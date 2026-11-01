package main

import (
	"fmt"
	"strconv"
	"strings"
)

// byteSize is a flag value for sizes like "500MB" or "20GB" (decimal units,
// as disk sizes are usually given) or "1GiB" (binary, as memory is). A plain
// number is bytes; "0" means no limit. It satisfies flag.Value, so it can be
// used with fs.Var.
type byteSize int64

var units = []struct {
	suffix string
	bytes  float64
}{ // longest suffixes first, so "MB" isn't read as "B"; binary units are
	// matched before decimal ones, and printed when a size is a round number of them
	{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
	{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
}

func (b *byteSize) Set(s string) error {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.bytes
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("want a size like 500MB or 20GB")
	}
	*b = byteSize(n * mult)
	return nil
}

func (b *byteSize) String() string {
	v := float64(*b)
	for _, u := range units[:3] { // TiB, GiB, MiB, when that's at most one decimal: 1.5GiB
		if v >= u.bytes && int64(v*10)%int64(u.bytes) == 0 {
			return strconv.FormatFloat(v/u.bytes, 'f', -1, 64) + strings.Replace(u.suffix, "IB", "iB", 1)
		}
	}
	for _, u := range units[4:] {
		if v >= u.bytes && u.bytes > 1 {
			return strconv.FormatFloat(v/u.bytes, 'f', -1, 64) + u.suffix
		}
	}
	return strconv.FormatInt(int64(*b), 10)
}
