-- Durations in this schema were stored as integer milliseconds, which cannot
-- express what it records. Measured on 61 decisions (docs/deadline-model.md
-- section 7), every stage's elapsed_ms held exactly two distinct values, 0
-- and 1, and total_elapsed_ms held three: the stages of an 80 ms budget run
-- in hundreds of microseconds, so the column rounded almost every one of
-- them to zero. No percentile or budget-utilisation figure is computable
-- from that, which is the one thing section 6 of the deadline model asks of
-- it.
--
-- DecisionLineage carries every one of these as a protobuf Duration, so
-- raw_lineage has held nanoseconds all along and nothing was lost; only the
-- queryable projection of it was. Nanoseconds are therefore a lossless
-- projection rather than a chosen precision, and BIGINT holds an 80 ms
-- budget in nanoseconds with eleven orders of magnitude to spare.
--
-- Budgets and deadlines convert too, even though they are configured in
-- whole milliseconds. Utilisation is elapsed over budget, and a schema that
-- mixes units in one ratio invites exactly the arithmetic mistake it would
-- then be used to investigate.
--
-- Existing rows are scaled rather than recomputed. They cannot be made more
-- precise than they were recorded; scaling preserves what was stored without
-- implying it was measured any more finely.

ALTER TABLE decisions RENAME COLUMN deadline_ms TO deadline_ns;
ALTER TABLE decisions RENAME COLUMN total_elapsed_ms TO total_elapsed_ns;
UPDATE decisions SET deadline_ns = deadline_ns * 1000000, total_elapsed_ns = total_elapsed_ns * 1000000;

ALTER TABLE agent_evaluations RENAME COLUMN observed_latency_ms TO observed_latency_ns;
UPDATE agent_evaluations SET observed_latency_ns = observed_latency_ns * 1000000;

ALTER TABLE execution_spans RENAME COLUMN elapsed_ms TO elapsed_ns;
ALTER TABLE execution_spans RENAME COLUMN budget_ms TO budget_ns;
UPDATE execution_spans SET elapsed_ns = elapsed_ns * 1000000, budget_ns = budget_ns * 1000000;

ALTER TABLE decision_failures RENAME COLUMN elapsed_ms TO elapsed_ns;
UPDATE decision_failures SET elapsed_ns = elapsed_ns * 1000000;
