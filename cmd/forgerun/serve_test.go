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
