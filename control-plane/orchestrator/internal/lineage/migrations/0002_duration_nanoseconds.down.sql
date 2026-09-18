-- Reverting loses precision that cannot be recovered: sub-millisecond spans
-- become zero again, which is the defect 0002 exists to fix.

UPDATE decisions SET deadline_ns = deadline_ns / 1000000, total_elapsed_ns = total_elapsed_ns / 1000000;
ALTER TABLE decisions RENAME COLUMN deadline_ns TO deadline_ms;
ALTER TABLE decisions RENAME COLUMN total_elapsed_ns TO total_elapsed_ms;

UPDATE agent_evaluations SET observed_latency_ns = observed_latency_ns / 1000000;
ALTER TABLE agent_evaluations RENAME COLUMN observed_latency_ns TO observed_latency_ms;

UPDATE execution_spans SET elapsed_ns = elapsed_ns / 1000000, budget_ns = budget_ns / 1000000;
ALTER TABLE execution_spans RENAME COLUMN elapsed_ns TO elapsed_ms;
ALTER TABLE execution_spans RENAME COLUMN budget_ns TO budget_ms;

UPDATE decision_failures SET elapsed_ns = elapsed_ns / 1000000;
ALTER TABLE decision_failures RENAME COLUMN elapsed_ns TO elapsed_ms;
