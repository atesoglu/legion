CREATE TABLE agent_definitions (
	agent_id      TEXT NOT NULL,
	version       TEXT NOT NULL,
	name          TEXT NOT NULL,
	description   TEXT NOT NULL DEFAULT '',
	system_prompt TEXT NOT NULL DEFAULT '',
	allowed_tools JSONB NOT NULL DEFAULT '[]',
	model_policy  JSONB NOT NULL DEFAULT '{}',
	configuration JSONB NOT NULL DEFAULT '{}',
	enabled       BOOLEAN NOT NULL DEFAULT FALSE,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (agent_id, version)
);

CREATE TABLE cases (
	case_id         TEXT PRIMARY KEY,
	decision_id     TEXT NOT NULL,
	transaction_id  TEXT NOT NULL,
	idempotency_key TEXT NOT NULL UNIQUE,
	status          TEXT NOT NULL,
	priority        INT NOT NULL DEFAULT 0,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE investigations (
	investigation_id    TEXT PRIMARY KEY,
	case_id             TEXT NOT NULL REFERENCES cases (case_id),
	status              TEXT NOT NULL,
	activated_agent_ids JSONB NOT NULL DEFAULT '[]',
	created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at        TIMESTAMPTZ
);
CREATE INDEX investigations_case_id_idx ON investigations (case_id);

CREATE TABLE tasks (
	task_id          TEXT PRIMARY KEY,
	investigation_id TEXT NOT NULL REFERENCES investigations (investigation_id),
	agent_id         TEXT NOT NULL,
	status           TEXT NOT NULL,
	attempt          INT NOT NULL DEFAULT 0,
	max_attempts     INT NOT NULL DEFAULT 3,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX tasks_investigation_id_idx ON tasks (investigation_id);

CREATE TABLE evidence (
	evidence_id      TEXT PRIMARY KEY,
	investigation_id TEXT NOT NULL REFERENCES investigations (investigation_id),
	source           TEXT NOT NULL,
	content          JSONB NOT NULL DEFAULT '{}',
	collected_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_findings (
	finding_id   TEXT PRIMARY KEY,
	task_id      TEXT NOT NULL REFERENCES tasks (task_id),
	agent_id     TEXT NOT NULL,
	evidence_ids JSONB NOT NULL DEFAULT '[]',
	observation  TEXT NOT NULL,
	hypothesis   TEXT NOT NULL,
	confidence   INT NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE tool_executions (
	tool_execution_id TEXT PRIMARY KEY,
	task_id           TEXT NOT NULL REFERENCES tasks (task_id),
	tool_name         TEXT NOT NULL,
	arguments         JSONB NOT NULL DEFAULT '{}',
	result            JSONB NOT NULL DEFAULT '{}',
	status            TEXT NOT NULL,
	duration_ms       BIGINT NOT NULL DEFAULT 0,
	executed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE investigation_audit_events (
	event_id    TEXT PRIMARY KEY,
	case_id     TEXT NOT NULL,
	event_type  TEXT NOT NULL,
	detail      JSONB NOT NULL DEFAULT '{}',
	occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX investigation_audit_events_case_id_idx ON investigation_audit_events (case_id);
