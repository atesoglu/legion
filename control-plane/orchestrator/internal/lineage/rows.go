package lineage

import (
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// decisionRow is one row of the decisions table.
// Durations are nanoseconds throughout, which is what the DecisionLineage
// Duration fields carry. Milliseconds rounded almost every stage of an 80 ms
// budget to zero -- see docs/deadline-model.md section 8 and migration 0002.
type decisionRow struct {
	ID               string
	TransactionID    string
	DecidedAtUnix    int64
	Decision         string
	AggregateScore   int32
	DegradationState string
	PolicyID         string
	PolicyVersion    string
	PolicyFallback   bool
	Shadow           bool
	DeadlineNs       int64
	TotalElapsedNs   int64
	RawLineage       []byte
}

// agentEvaluationRow is one row of agent_evaluations: one per agent fanned
// out to, merging what happened (AgentEvaluation) with how it factored into
// the score (SignalContribution).
type agentEvaluationRow struct {
	AgentID              string
	OutcomeKind          string // "SIGNAL" or "FAILURE"
	Score                int32
	Confidence           int32
	WeightBasisPoints    int32
	WeightedContribution int32
	Included             bool
	ExclusionReason      string
	AgentVersion         string
	FailureKind          string
	FailureComponent     string
	FailureMessage       string
	FailureRetryable     bool
	ObservedLatencyNs    int64
}

type executionSpanRow struct {
	Stage     string
	ElapsedNs int64
	BudgetNs  int64
}

type decisionFailureRow struct {
	Kind      string
	Component string
	Message   string
	ElapsedNs int64
	Retryable bool
}

type governedVersionRow struct {
	Artefact string
	Name     string
	Version  string
	Digest   string
}

// rows is every table row one DecisionLineage entry produces.
type rows struct {
	decision         decisionRow
	agentEvaluations []agentEvaluationRow
	executionSpans   []executionSpanRow
	decisionFailures []decisionFailureRow
	governedVersions []governedVersionRow
}

// buildRows maps one DecisionLineage onto the normalised schema (ADR-018).
// It is a pure function of its input, the same discipline
// evaluation.buildLineage holds itself to, so the mapping is unit-testable
// without a database.
func buildRows(entry *riskv1.DecisionLineage, rawLineage []byte) rows {
	outcome := entry.GetOutcome()

	contributionsByAgent := make(map[string]*riskv1.SignalContribution, len(outcome.GetContributions()))
	for _, contribution := range outcome.GetContributions() {
		contributionsByAgent[contribution.GetAgentId()] = contribution
	}

	evaluations := make([]agentEvaluationRow, 0, len(entry.GetEvaluations()))
	for _, evaluation := range entry.GetEvaluations() {
		evaluations = append(evaluations, buildAgentEvaluationRow(evaluation, contributionsByAgent[evaluation.GetAgentId()]))
	}

	spans := make([]executionSpanRow, 0, len(entry.GetSpans()))
	for _, span := range entry.GetSpans() {
		spans = append(spans, executionSpanRow{
			Stage:     span.GetStage(),
			ElapsedNs: span.GetElapsed().AsDuration().Nanoseconds(),
			BudgetNs:  span.GetBudget().AsDuration().Nanoseconds(),
		})
	}

	failures := make([]decisionFailureRow, 0, len(entry.GetFailures()))
	for _, failure := range entry.GetFailures() {
		failures = append(failures, decisionFailureRow{
			Kind:      failure.GetKind().String(),
			Component: failure.GetComponent(),
			Message:   failure.GetMessage(),
			ElapsedNs: failure.GetElapsed().AsDuration().Nanoseconds(),
			Retryable: failure.GetRetryable(),
		})
	}

	return rows{
		decision: decisionRow{
			ID:               entry.GetDecisionId(),
			TransactionID:    entry.GetTransactionId().GetValue(),
			DecidedAtUnix:    entry.GetDecidedAt().AsTime().Unix(),
			Decision:         outcome.GetDecision().String(),
			AggregateScore:   int32(outcome.GetAggregateScore()),
			DegradationState: outcome.GetDegradation().String(),
			PolicyID:         outcome.GetPolicy().GetPolicyId(),
			PolicyVersion:    outcome.GetPolicy().GetVersion().GetVersion(),
			PolicyFallback:   outcome.GetPolicy().GetFallback(),
			Shadow:           entry.GetShadow(),
			DeadlineNs:       entry.GetDeadline().AsDuration().Nanoseconds(),
			TotalElapsedNs:   entry.GetTotalElapsed().AsDuration().Nanoseconds(),
			RawLineage:       rawLineage,
		},
		agentEvaluations: evaluations,
		executionSpans:   spans,
		decisionFailures: failures,
		governedVersions: buildGovernedVersionRows(entry.GetVersions()),
	}
}

func buildAgentEvaluationRow(evaluation *riskv1.AgentEvaluation, contribution *riskv1.SignalContribution) agentEvaluationRow {
	row := agentEvaluationRow{
		AgentID:              evaluation.GetAgentId(),
		WeightBasisPoints:    int32(contribution.GetWeightBasisPoints()),
		WeightedContribution: int32(contribution.GetWeightedContribution()),
		Included:             contribution.GetIncluded(),
		ExclusionReason:      contribution.GetExclusionReason().String(),
		ObservedLatencyNs:    evaluation.GetObservedLatency().AsDuration().Nanoseconds(),
	}
	// An included signal's own score is authoritative; an excluded one still
	// carries a score, but exclusion_reason is what explains why it did not
	// count, and contribution.score reflects the aggregation's own view.
	row.Score = int32(contribution.GetScore())

	if signal := evaluation.GetSignal(); signal != nil {
		row.OutcomeKind = "SIGNAL"
		row.Confidence = int32(signal.GetConfidence())
		row.AgentVersion = signal.GetAgentVersion().GetVersion()
		return row
	}

	failure := evaluation.GetFailure()
	row.OutcomeKind = "FAILURE"
	row.FailureKind = failure.GetKind().String()
	row.FailureComponent = failure.GetComponent()
	row.FailureMessage = failure.GetMessage()
	row.FailureRetryable = failure.GetRetryable()
	return row
}

// buildGovernedVersionRows emits one row per artefact that actually
// participated. An artefact with no name and no version is absent (the
// behavioural agent's model/prompt/output_schema when it did not run), not
// a row worth keeping.
func buildGovernedVersionRows(versions *riskv1.GovernedVersions) []governedVersionRow {
	var out []governedVersionRow

	for _, agent := range versions.GetAgents() {
		out = append(out, versionRow("agent", agent))
	}

	singular := []struct {
		artefact string
		version  *commonv1.Version
	}{
		{"policy", versions.GetPolicy()},
		{"feature_catalogue", versions.GetFeatureCatalogue()},
		{"model", versions.GetModel()},
		{"prompt", versions.GetPrompt()},
		{"output_schema", versions.GetOutputSchema()},
		{"contract", versions.GetContract()},
	}
	for _, entry := range singular {
		if row, ok := versionRowIfPresent(entry.artefact, entry.version); ok {
			out = append(out, row)
		}
	}

	return out
}

func versionRow(artefact string, version *commonv1.Version) governedVersionRow {
	return governedVersionRow{
		Artefact: artefact,
		Name:     version.GetName(),
		Version:  version.GetVersion(),
		Digest:   version.GetDigest(),
	}
}

// versionRowIfPresent reports false for a Version that never participated:
// GovernedVersions leaves model/prompt/output_schema entirely unset unless
// the behavioural agent ran, and an empty row would misrepresent that as a
// governed artefact with an empty name.
func versionRowIfPresent(artefact string, version *commonv1.Version) (governedVersionRow, bool) {
	if version.GetName() == "" && version.GetVersion() == "" {
		return governedVersionRow{}, false
	}
	return versionRow(artefact, version), true
}
