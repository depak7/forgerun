package main

import (
	"fmt"
	"strconv"
	"strings"
)

// byteSize is a flag value for sizes like "500MB" or "20GB" (decimal units,
// as disk sizes are usually given). A plain number is bytes; "0" means no
// limit. It satisfies flag.Value, so it can be used with fs.Var.
type byteSize int64

var units = []struct {
	suffix string
	bytes  float64
}{ // longest suffixes first, so "MB" isn't read as "B"
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
	for _, u := range units {
		if v := float64(*b); v >= u.bytes && u.bytes > 1 {
			return strconv.FormatFloat(v/u.bytes, 'f', -1, 64) + u.suffix
		}
	}
	return strconv.FormatInt(int64(*b), 10)
}
