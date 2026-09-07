//go:build e2e

package e2e

import (
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
