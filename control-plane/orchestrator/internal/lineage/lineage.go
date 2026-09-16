// Package lineage persists DecisionLineage so that a decision made today can
// be reconstructed tomorrow. It is what unblocks replay (ADR-013), evaluation
// and shadow mode (ADR-012).
//
// ADR-007 places the lineage store in PostgreSQL: write-heavy, queried
// analytically, and never read on the decision's critical path. Record
// therefore never blocks: it enqueues and returns, and a background writer
// owns the actual I/O. A store that cannot keep up drops entries rather than
// slowing or failing a decision that has already been made — the same
// posture the feature store takes on the read side.
package lineage

import (
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// Store persists decision lineage off the hot path.
type Store interface {
	// Record enqueues entry for persistence. It must not block the caller on
	// I/O; a full queue drops the entry rather than applying backpressure to
	// a decision that has already been returned to the caller.
	Record(entry *riskv1.DecisionLineage)

	// Close drains the queue and releases the underlying connection.
	Close()
}

// queueDepth bounds how many lineage entries may wait for the writer before
// Record starts dropping them under sustained overload.
const queueDepth = 256
