package main

import (
	"context"
	"strings"
	"testing"

	"github.com/depak7/forgerun/internal/models"
	"github.com/depak7/forgerun/internal/store"
	"github.com/depak7/forgerun/internal/store/memory"
)

// disabledStore reports every agent as disabled.
type disabledStore struct{ store.Store }

func (d disabledStore) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	a, err := d.Store.GetAgent(ctx, id)
	a.Enabled = false
	return a, err
}

func TestCheckAgent(t *testing.T) {
	ctx := context.Background()
	known := memory.New(models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", IsDefault: true, AgentDefault: true})

	if err := checkAgent(ctx, known, "cursor"); err != nil {
		t.Errorf("known agent: %v", err)
	}
	if err := checkAgent(ctx, memory.New(), "cursor"); err == nil || !strings.Contains(err.Error(), "not in the agents table") {
		t.Errorf("missing agent: got %v", err)
	}
	if err := checkAgent(ctx, disabledStore{known}, "cursor"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled agent: got %v", err)
	}
}

func TestCheckModels(t *testing.T) {
	ctx := context.Background()
	cursor := memory.New(
		models.Variant{ID: "auto", AgentID: "cursor", Family: "auto", IsDefault: true, AgentDefault: true, ZDR: true},
		models.Variant{ID: "opus-medium", AgentID: "cursor", Family: "opus", Effort: "medium", IsDefault: true, ZDR: true},
		models.Variant{ID: "fable-high", AgentID: "cursor", Family: "fable", Effort: "high", IsDefault: true},
	)
	tests := []struct {
		name         string
		db           store.Store
		defaultModel string
		requireZDR   bool
		want         string // effective default; "" means startup must fail
		wantErr      string
	}{
		{"agent default", cursor, "", false, "auto", ""},
		{"server default", cursor, "opus", false, "opus-medium", ""},
		{"non-ZDR default allowed", cursor, "fable", false, "fable-high", ""},
		{"typo in -model", cursor, "opsu", false, "", `-model "opsu"`},
		{"-require-zdr removes the chosen default", cursor, "fable", true, "", `-model "fable"`},
		{"agent without a default", memory.New(models.Variant{ID: "x", AgentID: "cursor", Family: "x", IsDefault: true}), "", false, "", "no usable default model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := checkModels(ctx, tc.db, "cursor", tc.defaultModel, tc.requireZDR)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("got %q, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
