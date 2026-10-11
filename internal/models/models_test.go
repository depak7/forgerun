package models

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Rows copied from the real seed, covering the shapes that matter.
var fixture = []Variant{
	{ID: "auto", Family: "auto", Name: "Auto", IsDefault: true, ZDR: true},

	// Both thinking and non-thinking, with fast; default high for both.
	{ID: "claude-opus-5-low", Family: "claude-opus-5", Name: "Claude Opus 5 1M Low", Effort: "low", ZDR: true},
	{ID: "claude-opus-5-high", Family: "claude-opus-5", Name: "Claude Opus 5 1M", Effort: "high", IsDefault: true, ZDR: true},
	{ID: "claude-opus-5-high-fast", Family: "claude-opus-5", Name: "Claude Opus 5 1M Fast", Effort: "high", Fast: true, ZDR: true},
	{ID: "claude-opus-5-thinking-high", Family: "claude-opus-5", Name: "Claude Opus 5 1M Thinking", Effort: "high", Thinking: true, IsDefault: true, ZDR: true},
	{ID: "claude-opus-5-thinking-xhigh", Family: "claude-opus-5", Name: "Claude Opus 5 1M Extra High Thinking", Effort: "xhigh", Thinking: true, ZDR: true},

	// A plain variant ("") is the default.
	{ID: "gpt-5.2", Family: "gpt-5.2", Name: "GPT-5.2", IsDefault: true, ZDR: true},
	{ID: "gpt-5.2-fast", Family: "gpt-5.2", Name: "GPT-5.2 Fast", Fast: true, ZDR: true},
	{ID: "gpt-5.2-high", Family: "gpt-5.2", Name: "GPT-5.2 High", Effort: "high", ZDR: true},
	{ID: "gpt-5.2-low", Family: "gpt-5.2", Name: "GPT-5.2 Low", Effort: "low", ZDR: true},

	// No zero data retention.
	{ID: "claude-fable-5-1-high", Family: "claude-fable-5-1", Name: "Claude Fable 5.1 1M (NO ZDR)", Effort: "high", IsDefault: true},

	// Added by hand without is_default: falls back to medium.
	{ID: "new-model-low", Family: "new-model", Name: "New Low", Effort: "low", ZDR: true},
	{ID: "new-model-medium", Family: "new-model", Name: "New Medium", Effort: "medium", ZDR: true},
}

func ptr(b bool) *bool { return &b }

func TestResolve(t *testing.T) {
	c := NewCatalog(fixture)
	tests := []struct {
		name   string
		choice Choice
		want   string
	}{
		{"empty uses default model", Choice{}, "auto"},
		{"auto", Choice{Model: "auto"}, "auto"},
		{"family default effort", Choice{Model: "claude-opus-5"}, "claude-opus-5-high"},
		{"explicit effort", Choice{Model: "claude-opus-5", Effort: "low"}, "claude-opus-5-low"},
		{"fast", Choice{Model: "claude-opus-5", Fast: ptr(true)}, "claude-opus-5-high-fast"},
		{"thinking default", Choice{Model: "claude-opus-5", Thinking: ptr(true)}, "claude-opus-5-thinking-high"},
		{"effort only exists with thinking", Choice{Model: "claude-opus-5", Effort: "xhigh"}, "claude-opus-5-thinking-xhigh"},
		{"plain default", Choice{Model: "gpt-5.2"}, "gpt-5.2"},
		{"plain fast", Choice{Model: "gpt-5.2", Fast: ptr(true)}, "gpt-5.2-fast"},
		{"fallback default medium", Choice{Model: "new-model"}, "new-model-medium"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := c.Resolve(tc.choice, "auto")
			if err != nil {
				t.Fatal(err)
			}
			if v.ID != tc.want {
				t.Errorf("got %s, want %s", v.ID, tc.want)
			}
		})
	}
}

func TestResolveRejects(t *testing.T) {
	c := NewCatalog(fixture)
	tests := []struct {
		name     string
		choice   Choice
		wantText string
	}{
		{"unknown model", Choice{Model: "gpt-9"}, `unknown model "gpt-9"`},
		{"unknown effort", Choice{Model: "claude-opus-5", Effort: "ultra"}, "effort=ultra"},
		{"xhigh without thinking", Choice{Model: "claude-opus-5", Effort: "xhigh", Thinking: ptr(false)}, "thinking xhigh"},
		{"low fast does not exist", Choice{Model: "claude-opus-5", Effort: "low", Fast: ptr(true)}, "high fast"},
		{"auto takes no options", Choice{Model: "auto", Effort: "high"}, "valid: default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Resolve(tc.choice, "auto")
			if !errors.Is(err, ErrInvalidChoice) {
				t.Fatalf("got %v, want ErrInvalidChoice", err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q should mention %q", err, tc.wantText)
			}
		})
	}
}

func TestFamilies(t *testing.T) {
	fams := NewCatalog(fixture).Families()
	var ids []string
	for _, f := range fams {
		ids = append(ids, f.ID)
	}
	want := []string{"auto", "claude-fable-5-1", "claude-opus-5", "gpt-5.2", "new-model"}
	if !slices.Equal(ids, want) {
		t.Fatalf("families = %v, want %v", ids, want)
	}

	opus := fams[2]
	if !slices.Equal(opus.Efforts, []string{"low", "high", "xhigh"}) || opus.DefaultEffort != "high" ||
		!opus.Thinking || !opus.Fast || !opus.ZDR || opus.Name != "Claude Opus 5 1M" {
		t.Errorf("claude-opus-5 = %+v", opus)
	}
	gpt := fams[3]
	if !slices.Equal(gpt.Efforts, []string{"low", "high"}) || gpt.DefaultEffort != "" || gpt.Thinking {
		t.Errorf("gpt-5.2 = %+v", gpt)
	}
	if fams[1].ZDR {
		t.Error("claude-fable-5-1 must not be ZDR")
	}
}

// Every variant must be reachable: resolving its own options gives it back.
func TestEveryVariantIsReachable(t *testing.T) {
	c := NewCatalog(fixture)
	for _, v := range fixture {
		got, err := c.Resolve(Choice{Model: v.Family, Effort: v.Effort, Thinking: ptr(v.Thinking), Fast: ptr(v.Fast)}, "auto")
		if err != nil || got.ID != v.ID {
			t.Errorf("%s: resolved to %q, %v", v.ID, got.ID, err)
		}
	}
}
