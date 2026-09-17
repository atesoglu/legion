-- ADR-018: normalised reproducibility schema, replacing the blob table
-- shipped in commit 47edb19. raw_lineage still carries the full
-- DecisionLineage protobuf message, byte-for-byte, as the authoritative
-- record replay (ADR-013) reconstructs from; the tables below are a
-- queryable projection of it, not a second source of truth.
DROP TABLE IF EXISTS decision_lineage;

CREATE TABLE decisions (
	id                TEXT PRIMARY KEY,
	transaction_id    TEXT NOT NULL,
	decided_at        TIMESTAMPTZ NOT NULL,
	decision          TEXT NOT NULL,
	aggregate_score   INT NOT NULL,
	degradation_state TEXT NOT NULL,
	policy_id         TEXT NOT NULL,
	policy_version    TEXT NOT NULL,
	policy_fallback   BOOLEAN NOT NULL,
	shadow            BOOLEAN NOT NULL,
	deadline_ms       BIGINT NOT NULL,
	total_elapsed_ms  BIGINT NOT NULL,
	raw_lineage       BYTEA NOT NULL
);
CREATE INDEX decisions_decided_at_idx ON decisions (decided_at);

-- One row per agent fanned out to for a decision, merging AgentEvaluation
-- (what happened when it was called) with SignalContribution (how it
-- factored into the score). outcome_kind is 'SIGNAL' or 'FAILURE'; the
-- failure_* columns are empty/false and the score/confidence/weight columns
-- are 0 whichever branch did not apply -- outcome_kind is the discriminator,
-- not NULL-ness.
CREATE TABLE agent_evaluations (
	id                    BIGSERIAL PRIMARY KEY,
	decision_id           TEXT NOT NULL REFERENCES decisions (id),
	agent_id              TEXT NOT NULL,
	outcome_kind          TEXT NOT NULL,
	score                 INT NOT NULL DEFAULT 0,
	confidence            INT NOT NULL DEFAULT 0,
	weight_basis_points   INT NOT NULL DEFAULT 0,
	weighted_contribution INT NOT NULL DEFAULT 0,
	included              BOOLEAN NOT NULL,
	exclusion_reason      TEXT NOT NULL DEFAULT '',
	agent_version         TEXT NOT NULL DEFAULT '',
	failure_kind          TEXT NOT NULL DEFAULT '',
	failure_component     TEXT NOT NULL DEFAULT '',
	failure_message       TEXT NOT NULL DEFAULT '',
	failure_retryable     BOOLEAN NOT NULL DEFAULT FALSE,
	observed_latency_ms   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX agent_evaluations_decision_id_idx ON agent_evaluations (decision_id);
CREATE INDEX agent_evaluations_agent_id_idx ON agent_evaluations (agent_id);

CREATE TABLE execution_spans (
	id          BIGSERIAL PRIMARY KEY,
	decision_id TEXT NOT NULL REFERENCES decisions (id),
	stage       TEXT NOT NULL,
	elapsed_ms  BIGINT NOT NULL,
	budget_ms   BIGINT NOT NULL
);
CREATE INDEX execution_spans_decision_id_idx ON execution_spans (decision_id);

-- The flattened DecisionLineage.failures list: every dependency failure
-- observed during the request, feature store included, not only the
-- per-agent failures already reflected in agent_evaluations.
CREATE TABLE decision_failures (
	id          BIGSERIAL PRIMARY KEY,
	decision_id TEXT NOT NULL REFERENCES decisions (id),
	kind        TEXT NOT NULL,
	component   TEXT NOT NULL,
	message     TEXT NOT NULL,
	elapsed_ms  BIGINT NOT NULL,
	retryable   BOOLEAN NOT NULL
);
CREATE INDEX decision_failures_decision_id_idx ON decision_failures (decision_id);

-- One row per governed artefact: 'agent' (one per successful agent),
-- 'policy', 'feature_catalogue', 'model', 'prompt', 'output_schema',
-- 'contract'. Artefacts that did not participate (model/prompt/output_schema
-- when the behavioural agent did not run) have no row at all.
CREATE TABLE governed_versions (
	id          BIGSERIAL PRIMARY KEY,
	decision_id TEXT NOT NULL REFERENCES decisions (id),
	artefact    TEXT NOT NULL,
	name        TEXT NOT NULL,
	version     TEXT NOT NULL,
	digest      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX governed_versions_decision_id_idx ON governed_versions (decision_id);
