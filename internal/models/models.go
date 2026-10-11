// Package models turns the rows of the models table into families a user can
// pick from, and resolves a request ("claude-opus-5-5, effort high, fast")
// to one exact agent model id.
package models

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Variant is one row of the models table: a single exact model id.
type Variant struct {
	ID        string // e.g. "claude-opus-5-5-high-fast"
	Agent     string
	Family    string // e.g. "claude-opus-5-5"
	Name      string
	Effort    string // "" = the family's plain variant
	Thinking  bool
	Fast      bool
	IsDefault bool // used when a request omits effort
	ZDR       bool
}

// Choice is what a request asks for. Empty strings and nil pointers mean
// "use the default".
type Choice struct {
	Model    string // a family id, or "auto"
	Effort   string
	Thinking *bool
	Fast     *bool
}

// Family is a group of variants as shown to users.
type Family struct {
	ID            string
	Name          string
	Efforts       []string // low → max; the plain variant ("") is not listed
	DefaultEffort string   // "" when the plain variant is the default
	Thinking      bool     // a thinking variant exists
	Fast          bool     // a fast variant exists
	ZDR           bool     // every variant has zero data retention
}

// ErrInvalidChoice means no variant matches the request.
var ErrInvalidChoice = errors.New("invalid model choice")

// effortRank orders efforts for display; unknown efforts sort last.
var effortRank = map[string]int{"": 0, "none": 1, "minimal": 2, "low": 3, "medium": 4, "high": 5, "xhigh": 6, "max": 7}

type key struct {
	effort         string
	thinking, fast bool
}

type family struct {
	Family
	variants map[key]Variant
	defaults map[bool]string // thinking → default effort
}

// Catalog answers questions about a fixed set of variants.
type Catalog struct {
	families map[string]*family
	byID     map[string]Variant
}

// NewCatalog groups variants into families.
func NewCatalog(vs []Variant) *Catalog {
	c := &Catalog{families: map[string]*family{}, byID: map[string]Variant{}}
	for _, v := range vs {
		f, ok := c.families[v.Family]
		if !ok {
			f = &family{
				Family:   Family{ID: v.Family, ZDR: true},
				variants: map[key]Variant{},
				defaults: map[bool]string{},
			}
			c.families[v.Family] = f
		}
		f.variants[key{v.Effort, v.Thinking, v.Fast}] = v
		c.byID[v.ID] = v
		f.Thinking = f.Thinking || v.Thinking
		f.Fast = f.Fast || v.Fast
		f.ZDR = f.ZDR && v.ZDR
		if v.IsDefault && !v.Fast {
			f.defaults[v.Thinking] = v.Effort
		}
	}
	for _, f := range c.families {
		f.finish()
	}
	return c
}

// finish derives the display fields once all variants are known.
func (f *family) finish() {
	efforts := map[string]bool{}
	for k := range f.variants {
		if k.effort != "" {
			efforts[k.effort] = true
		}
	}
	f.Efforts = slices.SortedFunc(maps.Keys(efforts), byRank)

	// A group with no is_default row (e.g. a hand-written INSERT) falls back
	// to its plain variant, then medium, then its lowest effort.
	for _, thinking := range []bool{false, true} {
		if _, ok := f.defaults[thinking]; ok {
			continue
		}
		for _, e := range append([]string{"", "medium"}, f.Efforts...) {
			if _, ok := f.variants[key{e, thinking, false}]; ok {
				f.defaults[thinking] = e
				break
			}
		}
	}

	f.DefaultEffort = f.defaults[false]
	thinking := false
	if _, ok := f.defaults[false]; !ok {
		f.DefaultEffort, thinking = f.defaults[true], true
	}
	if v, ok := f.variants[key{f.DefaultEffort, thinking, false}]; ok {
		f.Name = v.Name
	} else {
		f.Name = f.ID
	}
}

// Families returns every family, sorted by id.
func (c *Catalog) Families() []Family {
	out := make([]Family, 0, len(c.families))
	for _, id := range slices.Sorted(maps.Keys(c.families)) {
		out = append(out, c.families[id].Family)
	}
	return out
}

// Variant looks up an exact model id.
func (c *Catalog) Variant(id string) (Variant, bool) {
	v, ok := c.byID[id]
	return v, ok
}

// Resolve picks the variant ch asks for. An empty ch.Model means
// defaultModel. Omitted thinking prefers a non-thinking variant; omitted
// effort uses the family default; omitted fast means not fast.
func (c *Catalog) Resolve(ch Choice, defaultModel string) (Variant, error) {
	name := ch.Model
	if name == "" {
		name = defaultModel
	}
	f, ok := c.families[name]
	if !ok {
		return Variant{}, fmt.Errorf("%w: unknown model %q", ErrInvalidChoice, name)
	}

	fast := ch.Fast != nil && *ch.Fast
	order := []bool{false, true}
	if ch.Thinking != nil {
		order = []bool{*ch.Thinking}
	}
	for _, thinking := range order {
		effort := ch.Effort
		if effort == "" {
			d, ok := f.defaults[thinking]
			if !ok {
				continue
			}
			effort = d
		}
		if v, ok := f.variants[key{effort, thinking, fast}]; ok {
			return v, nil
		}
	}
	return Variant{}, fmt.Errorf("%w: %s has no variant matching %s; valid: %s",
		ErrInvalidChoice, name, describe(ch), f.valid())
}

// valid lists every variant of f, e.g. "low, high, high fast, thinking max".
func (f *family) valid() string {
	keys := slices.SortedFunc(maps.Keys(f.variants), func(a, b key) int {
		switch {
		case a.thinking != b.thinking:
			return boolCmp(a.thinking, b.thinking)
		case a.effort != b.effort:
			return byRank(a.effort, b.effort)
		default:
			return boolCmp(a.fast, b.fast)
		}
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k.String())
	}
	return strings.Join(parts, ", ")
}

func (k key) String() string {
	var words []string
	if k.thinking {
		words = append(words, "thinking")
	}
	if k.effort == "" {
		words = append(words, "default")
	} else {
		words = append(words, k.effort)
	}
	if k.fast {
		words = append(words, "fast")
	}
	return strings.Join(words, " ")
}

// describe renders what was asked for, for error messages.
func describe(ch Choice) string {
	var parts []string
	if ch.Effort != "" {
		parts = append(parts, "effort="+ch.Effort)
	}
	if ch.Thinking != nil {
		parts = append(parts, fmt.Sprintf("thinking=%t", *ch.Thinking))
	}
	if ch.Fast != nil {
		parts = append(parts, fmt.Sprintf("fast=%t", *ch.Fast))
	}
	if len(parts) == 0 {
		return "the defaults"
	}
	return strings.Join(parts, " ")
}

func byRank(a, b string) int {
	ra, okA := effortRank[a]
	rb, okB := effortRank[b]
	switch {
	case okA && okB:
		return ra - rb
	case okA:
		return -1
	case okB:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}
