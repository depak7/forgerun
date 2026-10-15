package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/depak7/forgerun/internal/sandbox"
)

var (
	// ErrUnknownAgent means forgerun has no adapter for the agent.
	ErrUnknownAgent = errors.New("agent isn't available on this server")
	// ErrNoCredential means the agent's credential isn't configured.
	ErrNoCredential = errors.New("no credential configured")
)

// Builtin returns every agent forgerun has an adapter for.
func Builtin() []Agent { return []Agent{&Cursor{}, &ClaudeCode{}} }

// Credentials gives an agent's credential when a task needs it. The value
// isn't kept: a key added or changed later is used by the next task.
type Credentials interface {
	// Value returns the credential called name (Agent.Credential) for
	// agentID, or an error wrapping ErrNoCredential.
	Value(ctx context.Context, agentID, name string) (string, error)
}

// EnvCredentials reads credentials from the host's environment variables.
type EnvCredentials struct{}

func (EnvCredentials) Value(_ context.Context, agentID, name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w for %s: set %s", ErrNoCredential, agentID, name)
}

// StaticCredentials is a fixed set of credentials by name, e.g. for tests.
type StaticCredentials map[string]string

func (c StaticCredentials) Value(_ context.Context, agentID, name string) (string, error) {
	if v := c[name]; v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w for %s: set %s", ErrNoCredential, agentID, name)
}

// Set is the agents a server can run, and where their credentials come
// from. Which agent runs is up to each request; credentials are only
// fetched for the task that needs them.
type Set struct {
	agents map[string]Agent
	creds  Credentials
}

// NewSet returns a Set of agents using creds (nil: no credentials at all).
func NewSet(creds Credentials, agents ...Agent) Set {
	s := Set{agents: map[string]Agent{}, creds: creds}
	for _, a := range agents {
		s.agents[a.ID()] = a
	}
	return s
}

// Get returns the agent with this id, or an error wrapping ErrUnknownAgent.
func (s Set) Get(id string) (Agent, error) {
	if a, ok := s.agents[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, id)
}

// IDs lists the agents, sorted.
func (s Set) IDs() []string { return slices.Sorted(maps.Keys(s.agents)) }

// Secrets returns what a VM for agent a needs: its credential, as proxy
// secrets. An agent that needs no credential gets none.
func (s Set) Secrets(ctx context.Context, a Agent) ([]sandbox.Secret, error) {
	name := a.Credential()
	if name == "" {
		return nil, nil
	}
	if s.creds == nil {
		return nil, fmt.Errorf("%w for %s: set %s", ErrNoCredential, a.ID(), name)
	}
	v, err := s.creds.Value(ctx, a.ID(), name)
	if err != nil {
		return nil, err
	}
	return a.Secrets(v), nil
}

// Check reports whether a task can run on agent id now: forgerun has an
// adapter for it and its credential is configured.
func (s Set) Check(ctx context.Context, id string) error {
	a, err := s.Get(id)
	if err != nil {
		return err
	}
	_, err = s.Secrets(ctx, a)
	return err
}
