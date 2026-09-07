// Package validation rejects malformed and unsafe requests at the edge.
//
// Zone 0 is untrusted, so every field is checked before it consumes platform
// resources: required fields are present, enums are specified, codes are the
// shape their standards define, and no string is unbounded.
//
// One check is a security control rather than a correctness one. Legion must
// never receive a primary account number, and the contract expresses that by
// carrying only PseudonymousId. A caller that has not pseudonymised is rejected
// here, at the boundary, rather than having cardholder data travel inward and
// be discovered in a lineage record later.
package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// Bounds on every free-form string the contract carries. Unbounded strings are
// a memory and cardinality risk, and category-like fields become metric labels.
const (
	MaxSegmentLength      = 64
	MaxSchemeLength       = 32
	MaxOsFamilyLength     = 32
	MaxCategoryCodeLength = 8
	MaxPolicyIDLength     = 64
	MaxAnnotations        = 8
	MaxAnnotationLength   = 512
	MaxKeyVersionLength   = 32
)

// ClockSkew is how far into the future an occurrence timestamp may sit.
const ClockSkew = 5 * time.Minute

// MaxAge is how old a transaction may be and still be evaluated. Velocity
// windows are computed against the occurrence time, so a stale timestamp
// produces a meaningless evaluation rather than a wrong one.
const MaxAge = 24 * time.Hour

var (
	// A pseudonym is the hex encoding of an HMAC-SHA-256 output.
	pseudonymPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	countryPattern   = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
	categoryPattern  = regexp.MustCompile(`^[0-9A-Za-z_-]+$`)
)

// Request validates one evaluation request against `now`.
func Request(request *gatewayv1.EvaluateTransactionRequest, now time.Time) error {
	if request.GetTransaction() == nil {
		return invalid("transaction is required")
	}
	if err := options(request.GetOptions()); err != nil {
		return err
	}
	return transaction(request.GetTransaction(), now)
}

func options(supplied *gatewayv1.EvaluationOptions) error {
	if supplied == nil {
		return nil
	}
	if len(supplied.GetPolicyId()) > MaxPolicyIDLength {
		return invalid("options.policy_id is too long")
	}
	if deadline := supplied.GetDeadline(); deadline != nil && deadline.AsDuration() <= 0 {
		return invalid("options.deadline must be positive")
	}
	return nil
}

func transaction(subject *riskv1.Transaction, now time.Time) error {
	if err := pseudonym(subject.GetId(), "transaction.id"); err != nil {
		return err
	}
	if err := occurredAt(subject.GetOccurredAt(), now); err != nil {
		return err
	}
	if subject.GetType() == riskv1.TransactionType_TRANSACTION_TYPE_UNSPECIFIED {
		return invalid("transaction.type must be specified")
	}
	if subject.GetChannel() == riskv1.Channel_CHANNEL_UNSPECIFIED {
		return invalid("transaction.channel must be specified")
	}
	if err := money(subject.GetAmount()); err != nil {
		return err
	}
	if err := account(subject.GetAccount()); err != nil {
		return err
	}
	if err := instrument(subject.GetInstrument()); err != nil {
		return err
	}
	if err := device(subject.GetDevice()); err != nil {
		return err
	}
	if err := location(subject.GetLocation()); err != nil {
		return err
	}
	if err := merchant(subject.GetMerchant()); err != nil {
		return err
	}
	return annotations(subject.GetAnnotations())
}

// pseudonym is the boundary that keeps cardholder data out of the platform.
func pseudonym(id *commonv1.PseudonymousId, field string) error {
	if id == nil {
		return invalid(field + " is required")
	}
	if id.GetDomain() == commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED {
		return invalid(field + ".domain must be specified")
	}
	if len(id.GetKeyVersion()) == 0 || len(id.GetKeyVersion()) > MaxKeyVersionLength {
		return invalid(field + ".key_version is required and must be short")
	}
	if !pseudonymPattern.MatchString(id.GetValue()) {
		// The rejected value is never echoed: it may be the very thing that
		// must not enter the platform.
		return invalid(field + ".value is not a pseudonymised identifier")
	}
	return nil
}

func occurredAt(supplied interface{ AsTime() time.Time }, now time.Time) error {
	if supplied == nil {
		return invalid("transaction.occurred_at is required")
	}
	occurred := supplied.AsTime()
	if occurred.After(now.Add(ClockSkew)) {
		return invalid("transaction.occurred_at is in the future")
	}
	if occurred.Before(now.Add(-MaxAge)) {
		return invalid("transaction.occurred_at is too old to evaluate")
	}
	return nil
}

func money(amount *commonv1.Money) error {
	if amount == nil {
		return invalid("transaction.amount is required")
	}
	if !currencyPattern.MatchString(amount.GetCurrencyCode()) {
		return invalid("transaction.amount.currency_code must be an ISO 4217 alphabetic code")
	}
	return nil
}

func account(supplied *riskv1.Account) error {
	if supplied == nil {
		return invalid("transaction.account is required")
	}
	if err := pseudonym(supplied.GetId(), "transaction.account.id"); err != nil {
		return err
	}
	if err := optionalCountry(supplied.GetHomeCountryCode(), "transaction.account.home_country_code"); err != nil {
		return err
	}
	return bounded(supplied.GetSegment(), MaxSegmentLength, "transaction.account.segment")
}

func instrument(supplied *riskv1.Instrument) error {
	if supplied == nil {
		return nil
	}
	if err := pseudonym(supplied.GetId(), "transaction.instrument.id"); err != nil {
		return err
	}
	if err := bounded(supplied.GetScheme(), MaxSchemeLength, "transaction.instrument.scheme"); err != nil {
		return err
	}
	return optionalCountry(supplied.GetIssuerCountryCode(), "transaction.instrument.issuer_country_code")
}

func device(supplied *riskv1.Device) error {
	if supplied == nil {
		return nil
	}
	if err := pseudonym(supplied.GetId(), "transaction.device.id"); err != nil {
		return err
	}
	if address := supplied.GetNetworkAddress(); address != nil {
		if err := pseudonym(address, "transaction.device.network_address"); err != nil {
			return err
		}
	}
	return bounded(supplied.GetOsFamily(), MaxOsFamilyLength, "transaction.device.os_family")
}

func location(supplied *riskv1.Location) error {
	if supplied == nil {
		return nil
	}
	if err := optionalCountry(supplied.GetCountryCode(), "transaction.location.country_code"); err != nil {
		return err
	}
	if lat := supplied.GetLatitude(); lat < -90 || lat > 90 {
		return invalid("transaction.location.latitude is out of range")
	}
	if lon := supplied.GetLongitude(); lon < -180 || lon > 180 {
		return invalid("transaction.location.longitude is out of range")
	}
	return nil
}

func merchant(supplied *riskv1.Merchant) error {
	if supplied == nil {
		return nil
	}
	if err := pseudonym(supplied.GetId(), "transaction.merchant.id"); err != nil {
		return err
	}
	if code := supplied.GetCategoryCode(); code != "" {
		if len(code) > MaxCategoryCodeLength || !categoryPattern.MatchString(code) {
			return invalid("transaction.merchant.category_code is not a category code")
		}
	}
	return optionalCountry(supplied.GetCountryCode(), "transaction.merchant.country_code")
}

// annotations are payer- and merchant-supplied text. They are bounded in count
// and length here; nothing downstream may treat them as instructions (T-05).
func annotations(supplied []*riskv1.UntrustedText) error {
	if len(supplied) > MaxAnnotations {
		return invalid("transaction.annotations has too many entries")
	}
	for _, annotation := range supplied {
		if len(annotation.GetValue()) > MaxAnnotationLength {
			return invalid("transaction.annotations value is too long")
		}
		if err := bounded(annotation.GetSource(), MaxSegmentLength, "transaction.annotations source"); err != nil {
			return err
		}
	}
	return nil
}

func optionalCountry(code, field string) error {
	if code == "" {
		return nil
	}
	if !countryPattern.MatchString(code) {
		return invalid(field + " must be an ISO 3166-1 alpha-2 code")
	}
	return nil
}

func bounded(value string, limit int, field string) error {
	if len(value) > limit {
		return invalid(fmt.Sprintf("%s exceeds %d characters", field, limit))
	}
	if strings.ContainsAny(value, "\x00") {
		return invalid(field + " contains a null byte")
	}
	return nil
}

func invalid(reason string) error {
	return status.Error(codes.InvalidArgument, reason)
}
