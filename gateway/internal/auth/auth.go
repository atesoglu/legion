// Package auth authenticates callers at the edge.
//
// Zone 0 is untrusted: everything arriving has to prove who it is before it can
// consume platform resources. This package answers only "which caller is this",
// which the rest of the gateway uses for rate limiting, logging and refusal.
//
// Phase 1 uses shared keys presented as bearer credentials. That is a weaker
// mechanism than the deployed target — mTLS with workload identity, or OIDC —
// which arrives in Phase 4 (ADR-011). It is written so that the call sites do
// not change when the mechanism does.
package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Caller is an authenticated identity. It is deliberately just a name: nothing
// downstream should be making decisions from anything richer than this yet.
type Caller struct {
	// ID is the stable caller identifier, safe to log and to key metrics by.
	ID string
}

// Authenticator resolves credentials to callers.
type Authenticator struct {
	// keys maps a presented credential to the caller it identifies.
	keys map[string]string
}

// New builds an authenticator from a credential set.
//
// An empty set is refused rather than treated as "allow everything": an edge
// service that authenticates nobody is not a degraded edge service, it is an
// open one.
func New(credentials map[string]string) (*Authenticator, error) {
	if len(credentials) == 0 {
		return nil, fmt.Errorf("auth: at least one caller credential must be configured")
	}

	keys := make(map[string]string, len(credentials))
	for caller, key := range credentials {
		switch {
		case strings.TrimSpace(caller) == "":
			return nil, fmt.Errorf("auth: caller identifier must not be empty")
		case len(key) < MinimumKeyLength:
			return nil, fmt.Errorf(
				"auth: credential for %q is shorter than %d characters", caller, MinimumKeyLength)
		}
		if existing, duplicate := keys[key]; duplicate {
			return nil, fmt.Errorf(
				"auth: %q and %q share a credential", existing, caller)
		}
		keys[key] = caller
	}
	return &Authenticator{keys: keys}, nil
}

// MinimumKeyLength is the shortest credential accepted. Short shared secrets are
// guessable, and a guessable credential is the whole of this control.
const MinimumKeyLength = 32

// Parse reads a credential set from `caller=key,caller=key`.
func Parse(spec string) (*Authenticator, error) {
	credentials := make(map[string]string)
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		caller, key, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("auth: credential entry is not a caller=key pair")
		}
		credentials[strings.TrimSpace(caller)] = strings.TrimSpace(key)
	}
	return New(credentials)
}

// Authenticate resolves the caller from a request's metadata.
//
// The error deliberately says nothing about why: distinguishing "no credential"
// from "wrong credential" hands an attacker a probe.
func (a *Authenticator) Authenticate(ctx context.Context) (Caller, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Caller{}, unauthenticated()
	}

	presented := ""
	for _, value := range md.Get("authorization") {
		if after, found := strings.CutPrefix(value, "Bearer "); found {
			presented = after
			break
		}
	}
	if presented == "" {
		return Caller{}, unauthenticated()
	}

	// Every configured credential is compared, and comparison is
	// constant-time, so neither the match nor its position is observable in
	// the time taken.
	matched := ""
	for key, caller := range a.keys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(presented)) == 1 {
			matched = caller
		}
	}
	if matched == "" {
		return Caller{}, unauthenticated()
	}
	return Caller{ID: matched}, nil
}

func unauthenticated() error {
	return status.Error(codes.Unauthenticated, "caller could not be authenticated")
}
