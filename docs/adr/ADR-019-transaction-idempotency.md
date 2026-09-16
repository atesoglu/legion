# ADR-019: Transaction idempotency keys

**Status:** Accepted
**Date:** 2026-09-16
**Phase:** 2 (contract change; implementation precedes case creation, since
Zone 6 depends on the same key to avoid duplicate cases)

## Context

`threat-model.md` T-13 has always named the intended mitigation for a replay
attack as "idempotency keys, transaction-level deduplication." Nothing
implements it. Concretely:

- `Transaction` carries pseudonymous identifiers (`Transaction.id` is a
  `PseudonymousId`) but no caller-supplied de-duplication key.
- `decision_id` is generated server-side, from random bytes, on every call —
  `control-plane/orchestrator/main.go`'s `newDecisionID`.
- Resubmitting an identical `Transaction` today produces a second
  `decision_id`, a second sentinel evaluation, and — since the lineage work
  shipped — a second `decision_lineage`/`decisions` row. Nothing detects or
  collapses the duplicate.

This was a tolerable gap while nothing downstream of a decision existed. It
stops being tolerable once Zone 6 (ADR-017) exists: a retried request must not
be allowed to open a second case for the same transaction, exactly as
`project-plan.md`'s source material requires ("duplicate requests must not
accidentally create duplicate financial decisions or duplicate investigations").

## Decision

`Transaction` gains a caller-supplied idempotency key:

```protobuf
// Caller-assigned identifier for this transaction, unique per caller.
// Required. Resubmission with the same key and caller returns the
// original decision rather than producing a new one.
string idempotency_key = N;
```

Enforcement:

1. **The gateway is where deduplication is decided**, not the orchestrator —
   consistent with the gateway being the only edge-facing component
   (`architecture.md` §6: it implements the same `DecisionService` as a
   policy-enforcing front).
2. `(caller_id, idempotency_key)` is the deduplication key. The same key from
   two different callers is not a collision; the same caller resubmitting the
   same key is.
3. A resubmission within a bounded retention window (24 hours, matching the
   gateway's existing "transaction older than a day is refused" rule already
   implied by velocity-window semantics) returns the **original**
   `EvaluateTransactionResponse`, not a fresh evaluation. It is not an error;
   a retry after a dropped response must succeed exactly like the first
   attempt from the caller's point of view.
4. A resubmission with the same key but a **materially different**
   transaction body (different amount, different account) is rejected with
   `INVALID_ARGUMENT` — a reused key is either a legitimate retry or a caller
   bug, and it must never silently evaluate the new body under the old
   key's cached response.
5. Case creation (ADR-017) reuses the same key: `CaseTrigger` carries
   `idempotency_key`, and the investigation controller upserts on it, so a
   duplicate trigger (the queue's own at-least-once delivery, per ADR-017)
   cannot open two cases for one transaction.

## Alternatives considered

**Derive an idempotency key from transaction content (a hash of amount,
account, timestamp).** Rejected: legitimate distinct transactions can
collide (same account, same amount, same second), and it gives the caller no
way to intentionally retry versus intentionally resubmit something that looks
similar. An explicit caller-assigned key is unambiguous; a derived one is a
guess.

**Deduplicate at the orchestrator instead of the gateway.** Rejected: the
gateway is already the sole edge boundary and the sole origin of the request
deadline (ADR at the deadline model); deduplication is the same category of
edge concern as rate limiting and authentication, and putting it one hop
further in adds a network round trip to every request for no benefit.

**No expiry — remember every key forever.** Rejected: unbounded retention of
a dedup table is an unbounded liability with no corresponding benefit; a
retry that arrives after 24 hours is not a retry, it is a new submission, and
should be treated as one.

**Make the key optional.** Rejected: an optional field is a field callers
will not set until they are debugging a production incident, at which point
it is too late to matter. Required-and-validated is what makes the guarantee
real.

**Ship with a migration window (accept-but-warn before enforce).** Rejected.
That concern only has weight once real, already-integrated callers exist
against Phase 1's contract; today the only caller is `test/e2e`'s own
fixtures. Building a warn-then-enforce path now means shipping and later
deleting logic that no real caller will ever exercise — pure carrying cost
for a migration that is not happening. The field is required and enforced
from its first version; a migration window is revisited if and when a real
external caller integrates before this ships (see Revisit if).

## Trade-offs

- **A new required field is additive at the wire level** (proto3 allows
  adding a field without breaking existing binaries), but it is a **contract
  change at the semantic level**: a request that omits it is rejected. With
  no real caller integrated yet, there is nothing to migrate, so this ships
  as a flag day, not a phased rollout.
- **The gateway needs a dedup store** (small, bounded-retention — Redis with
  a TTL is the natural fit, reusing operational familiarity from the feature
  store without sharing its instance, for the same isolation reason ADR-017
  gives for the task queue).
- **"Materially different body, same key" detection requires comparing
  requests**, which is a small but real piece of logic that must be kept
  correct — a false negative here (treating a different transaction as a
  duplicate) is a worse failure than a false positive (rejecting a
  legitimate retry that happened to vary a field).

## Consequences

- `threat-model.md` T-13's mitigation moves from "planned" to "specified
  here"; it remains unimplemented until this ADR's Phase lands.
- `EvaluateTransactionRequest`/`Transaction`'s contract changes; the field is
  additive at the wire level, so `buf breaking` is not a concern, but every
  caller must supply it from the first deployed version — there is no
  grace period.
- Zone 6 case creation (ADR-017) is idempotent by construction from its
  first version, rather than needing a retrofit once duplicate cases are
  observed in the wild.

## Revisit if

- Callers cannot practically guarantee key uniqueness on their side (for
  example, a caller with no natural request identifier of its own) — in which
  case a gateway-issued idempotency token, exchanged before the real request,
  is the fallback, at the cost of an extra round trip.
- A real external caller integrates against the unenforced field before this
  ships — in which case the accept-but-warn migration window rejected above
  becomes the right call after all, and should be added back rather than
  assumed away.
