// Package forgerun holds what the repo root keeps for others to read,
// so the binary serves the same text GitHub shows.
package forgerun

import _ "embed"

// LLMs and LLMsFull are llms.txt and llms-full.txt: a short and a full
// summary of forgerun for agents (llmstxt.org). They link the public demo
// as https://forgerun.depak.dev, which a server swaps for its own address.
var (
	//go:embed llms.txt
	LLMs string
	//go:embed llms-full.txt
	LLMsFull string
)
