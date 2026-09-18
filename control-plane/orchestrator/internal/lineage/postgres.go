package lineage

import (
	"context"
	"embed"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	"github.com/atesoglu/legion/internal/platform/migrate"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsTable is private to the lineage schema (ADR-018): lineage and
// investigation (ADR-017) may share a Postgres instance, and each schema's
// migration history must not collide with the other's.
const migrationsTable = "schema_migrations_lineage"

// writeTimeout bounds a single lineage write or migration run, so a slow or
// unreachable database degrades lineage rather than leaking goroutines.
const writeTimeout = 5 * time.Second

// PostgresStore is the Store used in every deployment. See ADR-007 and
// ADR-018 (the normalised schema this writes into).
type PostgresStore struct {
	pool  *pgxpool.Pool
	dsn   string
	queue chan *riskv1.DecisionLineage
	done  chan struct{}
	log   *slog.Logger
}

// NewPostgresStore opens a connection pool and starts the background writer.
//
// It does not verify connectivity, the same posture the feature store takes:
// a database that is down must not block startup. Migration and every write
// are attempted from the background writer instead, and a failure there is
// logged, not raised, because lineage is diagnostic and must never become a
// reason a decision fails.
func NewPostgresStore(dsn string, log *slog.Logger) (*PostgresStore, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, err
	}

	store := &PostgresStore{
		pool:  pool,
		dsn:   dsn,
		queue: make(chan *riskv1.DecisionLineage, queueDepth),
		done:  make(chan struct{}),
		log:   log,
	}
	go store.run()
	return store, nil
}

func (s *PostgresStore) run() {
	if err := migrate.Run(migrationsFS, s.dsn, migrationsTable); err != nil {
		s.log.Warn("lineage schema migration failed; writes will fail until it succeeds", "error", err)
	}

	for entry := range s.queue {
		s.write(entry)
	}
	close(s.done)
}

// write persists one DecisionLineage as a transaction across the five
// normalised tables (ADR-018), plus the raw marshalled message on decisions
// as the authoritative, byte-for-byte record replay reconstructs from.
func (s *PostgresStore) write(entry *riskv1.DecisionLineage) {
	raw, err := proto.Marshal(entry)
	if err != nil {
		s.log.Warn("lineage entry could not be serialised", "decision_id", entry.GetDecisionId(), "error", err)
		return
	}
	r := buildRows(entry, raw)

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	err = s.pool.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := insertAll(ctx, tx, r); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	if err != nil {
		s.log.Warn("lineage write failed", "decision_id", entry.GetDecisionId(), "error", err)
	}
}

func insertAll(ctx context.Context, tx pgx.Tx, r rows) error {
	d := r.decision
	tag, err := tx.Exec(ctx, `
		INSERT INTO decisions (
			id, transaction_id, decided_at, decision, aggregate_score, degradation_state,
			policy_id, policy_version, policy_fallback, shadow, deadline_ns, total_elapsed_ns, raw_lineage
		) VALUES ($1, $2, to_timestamp($3), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO NOTHING`,
		d.ID, d.TransactionID, d.DecidedAtUnix, d.Decision, d.AggregateScore, d.DegradationState,
		d.PolicyID, d.PolicyVersion, d.PolicyFallback, d.Shadow, d.DeadlineNs, d.TotalElapsedNs, d.RawLineage,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Already recorded by a previous attempt; the child tables would
		// already exist too, so there is nothing more to insert.
		return nil
	}

	for _, e := range r.agentEvaluations {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent_evaluations (
				decision_id, agent_id, outcome_kind, score, confidence, weight_basis_points,
				weighted_contribution, included, exclusion_reason, agent_version,
				failure_kind, failure_component, failure_message, failure_retryable, observed_latency_ns
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			d.ID, e.AgentID, e.OutcomeKind, e.Score, e.Confidence, e.WeightBasisPoints,
			e.WeightedContribution, e.Included, e.ExclusionReason, e.AgentVersion,
			e.FailureKind, e.FailureComponent, e.FailureMessage, e.FailureRetryable, e.ObservedLatencyNs,
		); err != nil {
			return err
		}
	}

	for _, span := range r.executionSpans {
		if _, err := tx.Exec(ctx, `
			INSERT INTO execution_spans (decision_id, stage, elapsed_ns, budget_ns)
			VALUES ($1, $2, $3, $4)`,
			d.ID, span.Stage, span.ElapsedNs, span.BudgetNs,
		); err != nil {
			return err
		}
	}

	for _, failure := range r.decisionFailures {
		if _, err := tx.Exec(ctx, `
			INSERT INTO decision_failures (decision_id, kind, component, message, elapsed_ns, retryable)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			d.ID, failure.Kind, failure.Component, failure.Message, failure.ElapsedNs, failure.Retryable,
		); err != nil {
			return err
		}
	}

	for _, version := range r.governedVersions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO governed_versions (decision_id, artefact, name, version, digest)
			VALUES ($1, $2, $3, $4, $5)`,
			d.ID, version.Artefact, version.Name, version.Version, version.Digest,
		); err != nil {
			return err
		}
	}

	return nil
}

// Record enqueues entry for persistence. See Store.
func (s *PostgresStore) Record(entry *riskv1.DecisionLineage) {
	select {
	case s.queue <- entry:
	default:
		s.log.Warn("lineage queue full; dropping entry", "decision_id", entry.GetDecisionId())
	}
}

// Close drains the queue, stops the writer and releases the pool.
func (s *PostgresStore) Close() {
	close(s.queue)
	<-s.done
	s.pool.Close()
}
