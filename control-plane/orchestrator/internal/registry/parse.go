package registry

import (
	"fmt"
	"strings"
)

// Parse builds a registry from a configuration string.
//
// The format is a comma-separated list of `id=endpoint` pairs, for example
// "velocity=velocity:9300,device=device:9400". This is the whole mechanism by
// which an agent joins the fan-out: no code changes, no redeploy of the
// orchestrator (ADR-014).
//
// Every agent parsed here is deterministic. Behavioural registration arrives
// with the behavioural agent itself, in Phase 2.
func Parse(spec string) (*Registry, error) {
	var agents []Agent

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		id, endpoint, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("registry: %q is not an id=endpoint pair", entry)
		}

		agents = append(agents, Agent{
			ID:       strings.TrimSpace(id),
			Endpoint: strings.TrimSpace(endpoint),
			Kind:     Deterministic,
		})
	}

	return New(agents)
}
