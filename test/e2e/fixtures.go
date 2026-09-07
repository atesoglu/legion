//go:build e2e

package e2e

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// Synthetic subjects and evidence.
//
// These are fixtures, not the fraud simulator: they exist to give the pipeline
// something well-formed to carry. The scenario DSL and labelled datasets are
// Phase 5 work.

func pseudonym(value string, domain commonv1.IdentifierDomain) *commonv1.PseudonymousId {
	return &commonv1.PseudonymousId{
		Value:      value,
		KeyVersion: "test-key-1",
		Domain:     domain,
	}
}

// transaction builds an ordinary, well-formed subject.
func transaction() *riskv1.Transaction {
	return &riskv1.Transaction{
		Id:         pseudonym("txn-0000000000000001", commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED),
		OccurredAt: timestamppb.New(time.Unix(1_700_000_000, 0)),
		Type:       riskv1.TransactionType_TRANSACTION_TYPE_PURCHASE,
		Channel:    riskv1.Channel_CHANNEL_ECOMMERCE,
		Amount:     &commonv1.Money{CurrencyCode: "EUR", MinorUnits: 4_250},
		Account: &riskv1.Account{
			Id:              pseudonym("acct-01", commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED),
			HomeCountryCode: "DE",
			Segment:         "retail",
		},
		Device: &riskv1.Device{
			Id:         pseudonym("dev-01", commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED),
			FormFactor: riskv1.DeviceFormFactor_DEVICE_FORM_FACTOR_MOBILE,
			OsFamily:   "android",
		},
		Location: &riskv1.Location{
			CountryCode:          "DE",
			AccuracyRadiusMetres: 5_000,
			Source:               riskv1.LocationSource_LOCATION_SOURCE_IP_GEOLOCATION,
		},
		Merchant: &riskv1.Merchant{
			Id:           pseudonym("mer-01", commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED),
			CategoryCode: "5411",
			CountryCode:  "DE",
		},
	}
}

// compromisedDevice returns a subject whose device intelligence reports
// tampering, which the device engine can assess with no features at all.
func compromisedDevice() *riskv1.Transaction {
	subject := transaction()
	subject.Device.IntegrityCompromised = true
	return subject
}

// Seeding the feature store.
//
// The key and value formats are written out here rather than imported from the
// orchestrator, because ADR-015 makes that package importable only from within
// the orchestrator. The duplication is deliberate and self-checking: this is an
// independent implementation of the same storage contract, and if the two ever
// disagree these tests fail rather than passing against a format nothing reads.

const (
	windowInstant = 1
	window5m      = 2
	window10m     = 3
	window1h      = 4
	window24h     = 5
)

// storedFeature is one seeded value.
type storedFeature struct {
	scope   string
	subject string
	name    string
	window  int
	value   int64
}

func (s storedFeature) key() string {
	return fmt.Sprintf("f:%s:%s:%s:%d", s.scope, s.subject, s.name, s.window)
}

func (s storedFeature) encoded(computedAt time.Time) string {
	return fmt.Sprintf("%d|%d|v1", s.value, computedAt.Unix())
}

const (
	fixtureAccount = "acct-01"
	fixtureDevice  = "dev-01"
)

// cardTestingHistory is the account history that makes the velocity engine
// report card testing: many authorisations in five minutes, all of them small.
// The device is deliberately absent from the store too, because a fresh device
// is what card testing usually arrives on.
func cardTestingHistory() []storedFeature {
	return []storedFeature{
		{"acct", fixtureAccount, "transactions", window5m, 8},
		{"acct", fixtureAccount, "transactions", window1h, 8},
		{"acct", fixtureAccount, "transactions", window24h, 8},
		{"acct", fixtureAccount, "amount", window5m, 1_600},
		{"acct", fixtureAccount, "failed_attempts", window10m, 0},
		{"acct", fixtureAccount, "unique_devices", window24h, 1},
		{"acct", fixtureAccount, "unique_locations", window24h, 1},
		{"dev", fixtureDevice, "accounts_per_device", window24h, 1},
	}
}

// settledHistory is an established, unremarkable account and device.
func settledHistory() []storedFeature {
	return []storedFeature{
		{"acct", fixtureAccount, "transactions", window5m, 1},
		{"acct", fixtureAccount, "transactions", window1h, 2},
		{"acct", fixtureAccount, "transactions", window24h, 5},
		{"acct", fixtureAccount, "amount", window5m, 4_250},
		{"acct", fixtureAccount, "failed_attempts", window10m, 0},
		{"acct", fixtureAccount, "unique_devices", window24h, 1},
		{"acct", fixtureAccount, "unique_locations", window24h, 1},
		{"dev", fixtureDevice, "accounts_per_device", window24h, 1},
		{"dev", fixtureDevice, "device_age", windowInstant, 5_000_000},
	}
}

func count(name string, window riskv1.FeatureWindow, value int64, freshness riskv1.FeatureFreshness) *riskv1.Feature {
	return &riskv1.Feature{
		Name:              name,
		Window:            window,
		Value:             &riskv1.FeatureValue{Kind: &riskv1.FeatureValue_Count{Count: value}},
		Freshness:         freshness,
		ComputedAt:        timestamppb.New(time.Unix(1_700_000_000, 0)),
		DefinitionVersion: "v1",
	}
}

const fresh = riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH

// quietVelocityFeatures describe an account doing nothing unusual.
func quietVelocityFeatures() *riskv1.FeatureSet {
	return &riskv1.FeatureSet{
		CatalogueVersion: "v1",
		Features: []*riskv1.Feature{
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_5M, 1, fresh),
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_1H, 2, fresh),
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_24H, 5, fresh),
			count("amount", riskv1.FeatureWindow_FEATURE_WINDOW_5M, 4_250, fresh),
			count("failed_attempts", riskv1.FeatureWindow_FEATURE_WINDOW_10M, 0, fresh),
		},
	}
}

// cardTestingFeatures describe many small authorisations in five minutes.
func cardTestingFeatures() *riskv1.FeatureSet {
	return &riskv1.FeatureSet{
		CatalogueVersion: "v1",
		Features: []*riskv1.Feature{
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_5M, 8, fresh),
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_1H, 8, fresh),
			count("transactions", riskv1.FeatureWindow_FEATURE_WINDOW_24H, 8, fresh),
			count("amount", riskv1.FeatureWindow_FEATURE_WINDOW_5M, 1_600, fresh),
			count("failed_attempts", riskv1.FeatureWindow_FEATURE_WINDOW_10M, 0, fresh),
		},
	}
}
