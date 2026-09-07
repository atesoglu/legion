package features

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

const (
	m5      = riskv1.FeatureWindow_FEATURE_WINDOW_5M
	h24     = riskv1.FeatureWindow_FEATURE_WINDOW_24H
	instant = riskv1.FeatureWindow_FEATURE_WINDOW_INSTANT
)

var reference = time.Unix(1_700_000_000, 0)

func store(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	s := NewRedisStore(client)
	s.now = func() time.Time { return reference }
	return s, server
}

func request(name string, window riskv1.FeatureWindow) Request {
	return Request{Name: name, Window: window, Scope: AccountScope, Subject: "acct-1"}
}

func fetchOne(t *testing.T, s *RedisStore, r Request) *riskv1.Feature {
	t.Helper()

	features, err := s.Fetch(context.Background(), []Request{r})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("Fetch returned %d features, want 1", len(features))
	}
	return features[0]
}

func TestAStoredValueIsFreshWithinTheStalenessBudget(t *testing.T) {
	s, server := store(t)
	r := request("transactions", m5)
	server.Set(Key(r), Encode(7, reference.Add(-time.Second), "v1"))

	feature := fetchOne(t, s, r)

	if feature.GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH {
		t.Errorf("freshness = %v, want FRESH", feature.GetFreshness())
	}
	if got := feature.GetValue().GetCount(); got != 7 {
		t.Errorf("count = %d, want 7", got)
	}
}

func TestAnOldValueIsStaleRatherThanDiscarded(t *testing.T) {
	s, server := store(t)
	r := request("transactions", m5)
	server.Set(Key(r), Encode(7, reference.Add(-StalenessBudget-time.Second), "v1"))

	feature := fetchOne(t, s, r)

	// An old count is usually better evidence than no count, provided the
	// agent is told which it received.
	if feature.GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_STALE {
		t.Errorf("freshness = %v, want STALE", feature.GetFreshness())
	}
	if got := feature.GetValue().GetCount(); got != 7 {
		t.Errorf("count = %d, want 7", got)
	}
}

func TestAMissingKeyIsAbsentNotUnavailable(t *testing.T) {
	s, _ := store(t)

	feature := fetchOne(t, s, request("transactions", m5))

	// The store answered; there is genuinely nothing there, as on a new
	// account. An agent may legitimately read this as zero.
	if feature.GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_ABSENT {
		t.Errorf("freshness = %v, want ABSENT", feature.GetFreshness())
	}
}

func TestAMalformedValueIsUnavailableNotZero(t *testing.T) {
	s, server := store(t)
	r := request("transactions", m5)
	server.Set(Key(r), "not-a-feature")

	feature := fetchOne(t, s, r)

	// Guessing zero here would invent evidence out of a storage defect.
	if feature.GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE {
		t.Errorf("freshness = %v, want UNAVAILABLE", feature.GetFreshness())
	}
}

func TestAStoreOutageIsAnError(t *testing.T) {
	s, server := store(t)
	server.Close()

	if _, err := s.Fetch(context.Background(), []Request{request("transactions", m5)}); err == nil {
		t.Fatal("Fetch succeeded against a closed store")
	}
}

func TestEveryRequestedFeatureIsReturnedInOrder(t *testing.T) {
	s, server := store(t)
	requests := []Request{
		request("transactions", m5),
		request("amount", h24),
		request("failed_attempts", riskv1.FeatureWindow_FEATURE_WINDOW_10M),
	}
	server.Set(Key(requests[1]), Encode(4_250, reference, "v1"))

	features, err := s.Fetch(context.Background(), requests)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(features) != len(requests) {
		t.Fatalf("Fetch returned %d features, want %d", len(features), len(requests))
	}
	for i, r := range requests {
		if features[i].GetName() != r.Name || features[i].GetWindow() != r.Window {
			t.Fatalf("feature %d = %s/%v, want %s/%v",
				i, features[i].GetName(), features[i].GetWindow(), r.Name, r.Window)
		}
	}
}

func TestTheWindowIsPartOfTheKey(t *testing.T) {
	if Key(request("transactions", m5)) == Key(request("transactions", h24)) {
		t.Fatal("two windows of one feature share a key")
	}
}

func TestScopesDoNotShareKeys(t *testing.T) {
	account := Request{Name: "x", Window: instant, Scope: AccountScope, Subject: "same"}
	device := Request{Name: "x", Window: instant, Scope: DeviceScope, Subject: "same"}

	if Key(account) == Key(device) {
		t.Fatal("an account and a device with the same identifier share a key")
	}
}

func TestUnavailableReportsEveryFeatureRatherThanFewer(t *testing.T) {
	requests := []Request{request("transactions", m5), request("amount", h24)}

	features := Unavailable(requests)

	if len(features) != len(requests) {
		t.Fatalf("Unavailable returned %d features, want %d", len(features), len(requests))
	}
	for _, feature := range features {
		if feature.GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE {
			t.Errorf("%s freshness = %v, want UNAVAILABLE", feature.GetName(), feature.GetFreshness())
		}
	}
}

func subject(accountID, deviceID string) *riskv1.Transaction {
	transaction := &riskv1.Transaction{}
	if accountID != "" {
		transaction.Account = &riskv1.Account{
			Id: &commonv1.PseudonymousId{Value: accountID},
		}
	}
	if deviceID != "" {
		transaction.Device = &riskv1.Device{
			Id: &commonv1.PseudonymousId{Value: deviceID},
		}
	}
	return transaction
}

func TestPlanCoversBothScopes(t *testing.T) {
	plan := Plan(subject("acct-1", "dev-1"))

	if len(plan) != len(catalogue) {
		t.Fatalf("plan has %d requests, want %d", len(plan), len(catalogue))
	}
	scopes := map[Scope]int{}
	for _, request := range plan {
		scopes[request.Scope]++
	}
	if scopes[AccountScope] == 0 || scopes[DeviceScope] == 0 {
		t.Fatalf("plan covers %v, want both scopes", scopes)
	}
}

func TestPlanSkipsAScopeWithNoIdentifier(t *testing.T) {
	plan := Plan(subject("acct-1", ""))

	for _, request := range plan {
		if request.Scope == DeviceScope {
			t.Fatal("plan asked the store about a device that was not supplied")
		}
	}
	if len(plan) == 0 {
		t.Fatal("plan dropped the account features too")
	}
}

func TestAccountAgeIsDerivedRatherThanFetched(t *testing.T) {
	transaction := subject("acct-1", "dev-1")
	transaction.Account.OpenedAt = timestamppb.New(reference.Add(-48 * time.Hour))

	derived := Derived(transaction, reference)

	if len(derived) != 1 {
		t.Fatalf("Derived returned %d features, want 1", len(derived))
	}
	if got := derived[0].GetValue().GetCount(); got != int64((48 * time.Hour).Seconds()) {
		t.Errorf("account_age = %d seconds, want %d", got, int64((48 * time.Hour).Seconds()))
	}
	// Nothing can make a value computed from the request itself stale.
	if derived[0].GetFreshness() != riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH {
		t.Errorf("freshness = %v, want FRESH", derived[0].GetFreshness())
	}

	for _, request := range Plan(transaction) {
		if request.Name == "account_age" {
			t.Fatal("account_age was also requested from the store")
		}
	}
}

func TestAnAccountWithNoOpeningDateDerivesNothing(t *testing.T) {
	if got := Derived(subject("acct-1", ""), reference); len(got) != 0 {
		t.Fatalf("Derived returned %d features, want 0", len(got))
	}
}
