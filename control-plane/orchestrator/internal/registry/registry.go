// Package registry holds the set of agents the orchestrator fans out to.
//
// The agent set is configuration, not code (ADR-014). Nothing in this package
// or its callers branches on a particular agent identifier: adding an agent is
// an entry here plus a deployment, never a change to the orchestrator.
package registry

import (
	"fmt"
	"sort"
	"strings"
)

// Kind determines which budget window an agent is invoked in.
//
// It is a property of the agent's cost, not of its identity: a deterministic
// agent is expected to answer in about a millisecond, a model-backed one in
// tens. See docs/deadline-model.md.
type Kind int

const (
	// Deterministic agents run in the shared short window.
	Deterministic Kind = iota
	// Behavioral agents run in the longer, less predictable window.
	Behavioral
)

// Agent is one entry in the registry.
type Agent struct {
	// ID is the stable agent identifier. It is the same string used as
	// agent_id in signals and lineage, as the service name in logs, and as the
	// workload identity (ADR-016).
	ID string

	// Endpoint is the gRPC target to dial.
	Endpoint string

	// Kind selects the budget window.
	Kind Kind
}

// Registry is a validated, ordered agent set.
type Registry struct {
	agents []Agent
}

// New validates and orders an agent set.
//
// Ordering is by identifier so that fan-out, assembly and any later summation
// are derived from the data rather than from a hand-maintained list.
//
// Refusing to start is deliberate: a registered agent that cannot be resolved
// is a misconfiguration, and configuration errors in this position are the
// failure mode that replaces the compile errors a hardcoded agent set would
// have given.
func New(agents []Agent) (*Registry, error) {
	if len(agents) == 0 {
		return nil, fmt.Errorf("registry: at least one agent must be configured")
	}

	ordered := make([]Agent, len(agents))
	copy(ordered, agents)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	seen := make(map[string]struct{}, len(ordered))
	for _, agent := range ordered {
		switch {
		case strings.TrimSpace(agent.ID) == "":
			return nil, fmt.Errorf("registry: agent identifier must not be empty")
		case strings.TrimSpace(agent.Endpoint) == "":
			return nil, fmt.Errorf("registry: agent %q has no endpoint", agent.ID)
		}
		if _, duplicate := seen[agent.ID]; duplicate {
			return nil, fmt.Errorf("registry: agent %q is registered twice", agent.ID)
		}
		seen[agent.ID] = struct{}{}
	}

	return &Registry{agents: ordered}, nil
}

// Agents returns every registered agent in identifier order.
func (r *Registry) Agents() []Agent {
	out := make([]Agent, len(r.agents))
	copy(out, r.agents)
	return out
}

// OfKind returns the registered agents invoked in one budget window.
func (r *Registry) OfKind(kind Kind) []Agent {
	var out []Agent
	for _, agent := range r.agents {
		if agent.Kind == kind {
			out = append(out, agent)
		}
	}
	return out
}

// IDs returns every registered agent identifier, in order.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent.ID)
	}
	return out
}
