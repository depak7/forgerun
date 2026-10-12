package models_test

import (
	"fmt"

	"github.com/depak7/forgerun/internal/models"
)

func ExampleCatalog_Resolve() {
	c := models.NewCatalog("cursor", []models.Variant{
		{ID: "claude-opus-5-high", AgentID: "cursor", Family: "claude-opus-5", Effort: "high", IsDefault: true},
		{ID: "claude-opus-5-high-fast", AgentID: "cursor", Family: "claude-opus-5", Effort: "high", Fast: true},
		{ID: "claude-opus-5-thinking-max", AgentID: "cursor", Family: "claude-opus-5", Effort: "max", Thinking: true},
	})
	fast := true

	v, _ := c.Resolve(models.Choice{Model: "claude-opus-5", Fast: &fast}, "auto")
	fmt.Println(v.ID)

	v, _ = c.Resolve(models.Choice{Model: "claude-opus-5", Effort: "max"}, "auto")
	fmt.Println(v.ID)

	_, err := c.Resolve(models.Choice{Model: "claude-opus-5", Effort: "low"}, "auto")
	fmt.Println(err)
	// Output:
	// claude-opus-5-high-fast
	// claude-opus-5-thinking-max
	// invalid model choice: claude-opus-5 has no variant matching effort=low; valid: high, high fast, thinking max
}
