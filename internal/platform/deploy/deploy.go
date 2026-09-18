// Package deploy reads the service set ADR-016 declares in
// deploy/services.yaml.
//
// The set is data rather than code so that adding a component is a data
// change, the deployment counterpart of ADR-014's rule for the agent set. One
// Helm chart iterates over it, the image build reads it, and a test asserts it
// still matches what the repository can actually build.
package deploy

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Language selects which of ADR-016's two Dockerfiles builds a service.
type Language string

const (
	Go   Language = "go"
	Rust Language = "rust"
)

// Zone is the trust zone a service is deployed into, which becomes its
// Kubernetes namespace (ADR-016 §2).
type Zone string

const (
	Edge          Zone = "edge"
	Control       Zone = "control"
	Data          Zone = "data"
	Agents        Zone = "agents"
	Investigation Zone = "investigation"
)

// Service is one deployable.
type Service struct {
	// ID is ADR-016 §1's single identifier. It is filled from the map key.
	ID string `yaml:"-"`

	Zone     Zone     `yaml:"zone"`
	Language Language `yaml:"language"`

	// Source is the build path: the Go main package, or the Rust binary
	// crate's directory.
	Source string `yaml:"source"`

	// Port is the gRPC listen port. Planned services have none.
	Port int `yaml:"port"`

	// Agent reports whether this service answers EvaluateRequest, which is
	// what decides whether it belongs in the orchestrator's fan-out.
	Agent bool `yaml:"agent"`

	// Planned marks an identifier fixed ahead of the code that will carry it.
	Planned bool `yaml:"planned"`
}

// Crate is the Rust crate name, which ADR-016 §1 fixes as legion-<id>.
func (s Service) Crate() string { return "legion-" + s.ID }

// Set is every declared service, keyed by identifier.
type Set struct {
	Services map[string]Service `yaml:"services"`
}

// Built returns the services that should exist as binaries today, sorted by
// identifier so that callers iterating them are deterministic.
func (s Set) Built() []Service {
	out := make([]Service, 0, len(s.Services))
	for _, service := range s.Services {
		if !service.Planned {
			out = append(out, service)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// dns1123 is ADR-016 §1's constraint on an identifier: it has to be usable
// unchanged as a Kubernetes object name and a label value.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Load reads and validates the service set.
//
// Validation is here rather than in whatever consumes the file because a
// malformed service set should fail once, loudly, at the point it is read --
// the same reasoning registry.New applies to the agent set.
func Load(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Set{}, fmt.Errorf("deploy: reading the service set: %w", err)
	}

	var set Set
	if err := yaml.Unmarshal(raw, &set); err != nil {
		return Set{}, fmt.Errorf("deploy: parsing %s: %w", path, err)
	}
	if len(set.Services) == 0 {
		return Set{}, fmt.Errorf("deploy: %s declares no services", path)
	}

	ports := make(map[int]string, len(set.Services))
	for id, service := range set.Services {
		service.ID = id
		if err := service.validate(); err != nil {
			return Set{}, err
		}
		if service.Planned {
			set.Services[id] = service
			continue
		}
		if owner, taken := ports[service.Port]; taken {
			return Set{}, fmt.Errorf("deploy: %s and %s both claim port %d", owner, id, service.Port)
		}
		ports[service.Port] = id
		set.Services[id] = service
	}
	return set, nil
}

func (s Service) validate() error {
	if !dns1123.MatchString(s.ID) {
		return fmt.Errorf("deploy: %q is not a DNS-1123 label, so it cannot be a Kubernetes object name", s.ID)
	}
	switch s.Zone {
	case Edge, Control, Data, Agents, Investigation:
	default:
		return fmt.Errorf("deploy: %s declares unknown zone %q", s.ID, s.Zone)
	}
	switch s.Language {
	case Go, Rust:
	default:
		return fmt.Errorf("deploy: %s declares unknown language %q", s.ID, s.Language)
	}
	if s.Source == "" {
		return fmt.Errorf("deploy: %s declares no source path", s.ID)
	}
	if s.Planned {
		if s.Port != 0 {
			return fmt.Errorf("deploy: %s is planned but claims port %d; a port for code that does not exist is an invented decision", s.ID, s.Port)
		}
		return nil
	}
	if s.Port < 9100 || s.Port > 9999 {
		return fmt.Errorf("deploy: %s declares port %d, outside Legion's 9100-9999 range", s.ID, s.Port)
	}
	return nil
}
