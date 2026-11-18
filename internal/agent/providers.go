package agent

import (
	"slices"
	"strings"

	"github.com/depak7/forgerun/internal/sandbox"
)

// providers are the model providers agents can use: the key each reads
// from its environment, and the only host the proxy sends that key to.
// A key is the same for every agent, so one OPENROUTER_API_KEY serves them
// all. Adding a provider is a line here, its name in the agents that
// support it, and its models' rows in the models table.
var providers = []struct{ name, key, host string }{
	{"anthropic", "ANTHROPIC_API_KEY", "api.anthropic.com"},
	{"openai", "OPENAI_API_KEY", "api.openai.com"},
	{"openrouter", "OPENROUTER_API_KEY", "openrouter.ai"},
	{"google", "GEMINI_API_KEY", "generativelanguage.googleapis.com"},
	{"xai", "XAI_API_KEY", "api.x.ai"},
	{"prime-inference", "PRIME_API_KEY", "api.pinference.ai"},
}

// providerKeys is the Providers capability for an agent that supports the
// named providers.
type providerKeys []string

// CredentialFor is the key of the model's provider, the part of its ID
// before the first slash; "" if the agent doesn't support that provider.
func (k providerKeys) CredentialFor(modelID string) string {
	provider, _, _ := strings.Cut(modelID, "/")
	if !slices.Contains(k, provider) {
		return ""
	}
	for _, pr := range providers {
		if pr.name == provider {
			return pr.key
		}
	}
	return ""
}

// Credentials names the keys of the agent's providers, sorted.
func (k providerKeys) Credentials() []string {
	var names []string
	for _, pr := range providers {
		if slices.Contains(k, pr.name) {
			names = append(names, pr.key)
		}
	}
	slices.Sort(names)
	return names
}

// SecretFor: the agent reads the key from its environment, where it's a
// placeholder; the proxy adds the real key for its provider's host only.
func (k providerKeys) SecretFor(name, value string) sandbox.Secret {
	s := sandbox.Secret{EnvVar: name, Value: value}
	for _, pr := range providers {
		if pr.key == name && slices.Contains(k, pr.name) {
			s.AllowHosts = []string{pr.host}
		}
	}
	return s
}
