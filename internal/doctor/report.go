package doctor

import (
	"encoding/json"
	"fmt"
	"io"
)

// Print writes the results as a readable report: one line per check, its
// fix under it, then a one-line verdict.
func Print(w io.Writer, role Role, rs []Result) {
	fmt.Fprintf(w, "forgerun doctor (%s)\n\n", role)
	for _, r := range rs {
		fmt.Fprintf(w, "  %-4s  %-22s %s\n", r.Status, r.Name, r.Detail)
		if r.Fix != "" {
			fmt.Fprintf(w, "        %-22s → %s\n", "", r.Fix)
		}
	}
	fails, warns := 0, 0
	for _, r := range rs {
		switch r.Status {
		case Fail:
			fails++
		case Warn:
			warns++
		}
	}
	fmt.Fprintln(w)
	switch {
	case fails > 0:
		fmt.Fprintf(w, "%d problem(s) to fix before forgerun can run here.\n", fails)
	case warns > 0:
		fmt.Fprintf(w, "Ready, with %d thing(s) worth a look.\n", warns)
	default:
		fmt.Fprintln(w, "Ready.")
	}
}

// PrintJSON writes the results as JSON, for scripts.
func PrintJSON(w io.Writer, role Role, rs []Result) error {
	return json.NewEncoder(w).Encode(struct {
		Role    Role     `json:"role"`
		Ready   bool     `json:"ready"`
		Results []Result `json:"results"`
	}{role, !Failed(rs), rs})
}
