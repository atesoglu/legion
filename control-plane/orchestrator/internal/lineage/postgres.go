package lineage

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// writeTimeout bounds a single lineage write or schema statement, so a slow or
// unreachable database degrades lineage rather than leaking goroutines.
const writeTimeout = 5 * time.Second

// schema is applied once at startup. There is no migration tool yet (Phase 1
// scope); this idempotent DDL is the whole of it, and it is intentionally
// small: the table stores the marshalled DecisionLineage as the source of
// truth, plus the columns an analytical query needs without deserialising it.
const schema = `
CREATE TABLE IF NOT EXISTS decision_lineage (
	decision_id    TEXT PRIMARY KEY,
	transaction_id TEXT NOT NULL,
	decided_at     TIMESTAMPTZ NOT NULL,
	decision       TEXT NOT NULL,
	shadow         BOOLEAN NOT NULL,
	lineage        BYTEA NOT NULL
);
CREATE INDEX IF NOT EXISTS decision_lineage_decided_at_idx ON decision_lineage (decided_at);
`

// PostgresStore is the Store used in every deployment. See ADR-007.
type PostgresStore struct {
	pool  *pgxpool.Pool
	queue chan *riskv1.DecisionLineage
	done  chan struct{}
	log   *slog.Logger
}

// NewPostgresStore opens a connection pool and starts the background writer.
//
// It does not verify connectivity, the same posture the feature store takes:
// a database that is down must not block startup. Schema application and
// every write are attempted from the background writer instead, and a
// failure there is logged, not raised, because lineage is diagnostic and must
// never become a reason a decision fails.
func NewPostgresStore(dsn string, log *slog.Logger) (*PostgresStore, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, err
	}

	store := &PostgresStore{
		pool:  pool,
		queue: make(chan *riskv1.DecisionLineage, queueDepth),
		done:  make(chan struct{}),
		log:   log,
	}
	go store.run()
	return store, nil
}

func (s *PostgresStore) run() {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	if _, err := s.pool.Exec(ctx, schema); err != nil {
		s.log.Warn("lineage schema could not be applied; writes will fail until it exists", "error", err)
	}
	cancel()

	for entry := range s.queue {
		s.write(entry)
	}
	close(s.done)
}

func (s *PostgresStore) write(entry *riskv1.DecisionLineage) {
	payload, err := proto.Marshal(entry)
	if err != nil {
		s.log.Warn("lineage entry could not be serialised", "decision_id", entry.GetDecisionId(), "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	_, err = s.pool.Exec(ctx, `
		INSERT INTO decision_lineage (decision_id, transaction_id, decided_at, decision, shadow, lineage)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (decision_id) DO NOTHING`,
		entry.GetDecisionId(),
		entry.GetTransactionId().GetValue(),
		entry.GetDecidedAt().AsTime(),
		entry.GetOutcome().GetDecision().String(),
		entry.GetShadow(),
		payload,
	)
	if err != nil {
		s.log.Warn("lineage write failed", "decision_id", entry.GetDecisionId(), "error", err)
	}
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
