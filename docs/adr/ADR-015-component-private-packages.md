# ADR-015: Component-private packages

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 1

## Context

Legion's services are currently entrypoints and nothing else. `gateway/main.go`
and `control-plane/orchestrator/main.go` declare `package main`, start a gRPC
server and stop cleanly. Phase 1 changes that: the gateway acquires
authentication, request validation and rate limiting; the orchestrator acquires
budget apportionment, fan-out, circuit breaking and evaluation assembly.

That code has to live somewhere, and the choice is made once. A directory
holding `package main` cannot also hold library packages, so the first Phase 1
package forces the decision whether anyone notices or not.

The default is `gateway/auth`. It works, it reads naturally, and it is
importable by the orchestrator, by the sentinel's Go tooling, and by anything
else added later. Nothing prevents that import, and nothing reports it.

This matters more here than in most systems. `docs/security-boundaries.md`
defines trust zones with hard rules between them — the gateway may reach nothing
but the orchestrator, only Zone 2 holds the feature-store credential, the
sentinel makes no outbound calls. Those rules are currently prose. Prose does
not fail a build.

Go already provides the enforcement, and it is positional: a package under a
directory named `internal` is importable only by code rooted at that
directory's parent. The mechanism is unused here beyond the repository-level
`internal/platform`.

## Decision

Code private to one component lives under that component's own `internal`
directory.

```text
gateway/
├── main.go                 package main
└── internal/
    ├── auth/               importable only from gateway/...
    ├── validation/
    └── ratelimit/

control-plane/orchestrator/
├── main.go
└── internal/
    ├── registry/           the agent registry (ADR-014)
    ├── budget/
    └── breaker/
```

Two tiers, with a rule for choosing between them:

| Location | For | Admission rule |
|---|---|---|
| `<component>/internal/...` | One component's own logic | The default. Everything starts here |
| `internal/platform/...` | Genuinely shared plumbing | Two or more components already need it. One consumer is not a shared package |

Promotion from the first to the second is deliberate: it happens when a second
consumer appears, not when one is anticipated. Rust follows the same shape
already — `crates/platform` is the shared crate, and per-crate modules are
private by default because Rust makes privacy the default rather than a
directory convention.

## Alternatives considered

**`cmd/` for binaries, `internal/<component>/` for their code.** The most common
Go layout, and unremarkable to any Go developer. Rejected because it flattens
the root into `cmd/` plus one undifferentiated `internal/` tree, which discards
the property that the top level reads as the deployment and trust topology. The
boundary would still be enforced, but it would no longer be visible.

**Flat packages under each component, such as `gateway/auth`.** Simplest, and
correct as long as nobody imports across components. Rejected because "as long
as nobody" is not an enforcement mechanism, and the cost of finding out is a
cross-zone dependency discovered after it has been depended upon.

**A linter forbidding cross-component imports.** Achieves the same end. Rejected
as strictly worse: another tool to configure and keep current, when the compiler
already does it for free and cannot be skipped.

## Trade-offs

- **Deeper paths.** `control-plane/orchestrator/internal/registry` is a mouthful,
  and import blocks get longer.
- **Genuinely shared code needs a deliberate move**, and that move is a visible
  diff rather than an ambient import. That is the point, but it is friction.
- **`internal` appears at two levels**, which is unusual enough to surprise
  someone reading the tree for the first time. It is legal Go and behaves
  exactly as the positional rule describes, but it needs the explanation this
  ADR provides.
- **Nothing stops a component depending on another component's *contract*,** nor
  should it: `protocol/gen/go` is public by design. This decision constrains
  implementation coupling, not contract coupling.

## Consequences

- A cross-component import of private code is a compile error, so the trust
  boundaries in `docs/security-boundaries.md` acquire partial mechanical
  enforcement inside the Go module.
- `internal/platform` stops being the default destination for shared-looking
  code and becomes a place code is promoted into.
- Test helpers follow the same rule. A helper used by two components is shared
  code and moves; a helper used by one stays private.
- The layout generalises to the services not yet built: the capability runtime
  and the behavioural agent each get their own `internal` from the first commit.

## Revisit if

- The Go module is split into several modules, at which point module boundaries
  enforce this directly and the nested `internal` directories become redundant.
- `internal/platform` grows to the point that it needs internal structure of its
  own, which would suggest the promotion rule is being applied too readily.
