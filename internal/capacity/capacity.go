// Package capacity is what a machine can give its sandboxes: its CPUs and
// memory, less a reserve it keeps for itself (the OS, Docker, forgerun),
// less what its open sandboxes were promised.
//
// Promised, not measured: a VM given 4 GiB may use all of it at any
// moment, so the whole 4 GiB counts as taken while the VM is open.
package capacity

// Resources is an amount of CPUs and memory.
type Resources struct {
	CPUs   int `json:"cpus"`
	MemMiB int `json:"mem_mib"`
}

// Plus adds o to r.
func (r Resources) Plus(o Resources) Resources {
	return Resources{CPUs: r.CPUs + o.CPUs, MemMiB: r.MemMiB + o.MemMiB}
}

// Minus takes o from r, stopping at zero.
func (r Resources) Minus(o Resources) Resources {
	return Resources{CPUs: max(r.CPUs-o.CPUs, 0), MemMiB: max(r.MemMiB-o.MemMiB, 0)}
}

// Fits reports whether o fits in r.
func (r Resources) Fits(o Resources) bool {
	return o.CPUs <= r.CPUs && o.MemMiB <= r.MemMiB
}

// Report is a machine's capacity, as a worker reports it.
type Report struct {
	Total    Resources `json:"total"`    // the whole machine
	Reserved Resources `json:"reserved"` // kept back for the machine itself
	Promised Resources `json:"promised"` // held by open sandboxes
}

// Free is what's left for new sandboxes.
func (r Report) Free() Resources {
	return r.Total.Minus(r.Reserved).Minus(r.Promised)
}
