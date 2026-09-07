// Package features fetches the windowed aggregates agents reason over.
//
// The store is read on every transaction and is a cache of derived state, not a
// system of record (ADR-007). Access lives here, in the control plane, because
// Zone 2 holds the only feature-store credential: no agent has a connection.
//
// The distinction this package exists to preserve is between a feature that is
// *absent* — the store answered and there genuinely is nothing, as on a new
// account — and one that is *unavailable*, where the store could not answer.
// Collapsing the two is how velocity rules fall silent during an outage.
package features

import (
	"time"

	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// Scope is the entity a feature is aggregated over.
type Scope string

const (
	// AccountScope aggregates over the account making the transaction.
	AccountScope Scope = "acct"
	// DeviceScope aggregates over the device it came from.
	DeviceScope Scope = "dev"
)

// Request identifies one feature to fetch.
type Request struct {
	// Name is the stable snake_case feature name. It does not encode the
	// window; Window does.
	Name string

	// Window is the rolling window the value is aggregated over.
	Window riskv1.FeatureWindow

	// Scope and Subject identify what the aggregate is over.
	Scope   Scope
	Subject string
}

// catalogueEntry is one feature definition in the current catalogue.
type catalogueEntry struct {
	name   string
	window riskv1.FeatureWindow
	scope  Scope
}

// catalogue is the initial feature set from docs/domain-model.md §6.
//
// Adding a feature is an entry here; no agent, orchestrator or store code
// changes. Removing one is a catalogue version change, because two values of
// the same name from different versions are not comparable.
var catalogue = []catalogueEntry{
	{"transactions", riskv1.FeatureWindow_FEATURE_WINDOW_5M, AccountScope},
	{"transactions", riskv1.FeatureWindow_FEATURE_WINDOW_1H, AccountScope},
	{"transactions", riskv1.FeatureWindow_FEATURE_WINDOW_24H, AccountScope},
	{"amount", riskv1.FeatureWindow_FEATURE_WINDOW_5M, AccountScope},
	{"amount", riskv1.FeatureWindow_FEATURE_WINDOW_24H, AccountScope},
	{"failed_attempts", riskv1.FeatureWindow_FEATURE_WINDOW_10M, AccountScope},
	{"unique_devices", riskv1.FeatureWindow_FEATURE_WINDOW_24H, AccountScope},
	{"unique_devices", riskv1.FeatureWindow_FEATURE_WINDOW_7D, AccountScope},
	{"unique_locations", riskv1.FeatureWindow_FEATURE_WINDOW_24H, AccountScope},
	{"accounts_per_device", riskv1.FeatureWindow_FEATURE_WINDOW_24H, DeviceScope},
	{"accounts_per_device", riskv1.FeatureWindow_FEATURE_WINDOW_30D, DeviceScope},
	{"device_age", riskv1.FeatureWindow_FEATURE_WINDOW_INSTANT, DeviceScope},
}

// CatalogueVersion identifies the definition set as a whole. It is recorded on
// every feature set and travels into decision lineage.
const CatalogueVersion = "v1"

// Plan returns the features to fetch for one subject.
//
// A scope whose identifier is missing contributes nothing: there is no point
// asking the store about a device that was not supplied. The resulting absence
// is reported to agents as unavailable, not as zero.
func Plan(subject *riskv1.Transaction) []Request {
	accountID := subject.GetAccount().GetId().GetValue()
	deviceID := subject.GetDevice().GetId().GetValue()

	requests := make([]Request, 0, len(catalogue))
	for _, entry := range catalogue {
		subjectID := accountID
		if entry.scope == DeviceScope {
			subjectID = deviceID
		}
		if subjectID == "" {
			continue
		}
		requests = append(requests, Request{
			Name:    entry.name,
			Window:  entry.window,
			Scope:   entry.scope,
			Subject: subjectID,
		})
	}
	return requests
}

// Derived returns the features computable from the subject alone.
//
// Account age is known from the transaction, so asking the store for it would
// be a round trip to learn something already in hand. It is FRESH by
// construction: nothing can make it stale.
func Derived(subject *riskv1.Transaction, now time.Time) []*riskv1.Feature {
	openedAt := subject.GetAccount().GetOpenedAt()
	if openedAt == nil {
		return nil
	}

	age := now.Sub(openedAt.AsTime())
	if age < 0 {
		age = 0
	}

	return []*riskv1.Feature{{
		Name:   "account_age",
		Window: riskv1.FeatureWindow_FEATURE_WINDOW_INSTANT,
		Value: &riskv1.FeatureValue{
			Kind: &riskv1.FeatureValue_Count{Count: int64(age.Seconds())},
		},
		Freshness:         riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH,
		DefinitionVersion: CatalogueVersion,
	}}
}
