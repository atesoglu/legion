package auth

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	acquirerKey = "acquirer-key-that-is-long-enough-to-pass"
	otherKey    = "another-key-that-is-long-enough-to-pass"
)

func authenticator(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(map[string]string{"acquirer-a": acquirerKey, "acquirer-b": otherKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func withCredential(credential string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", credential))
}

func TestAValidCredentialIdentifiesItsCaller(t *testing.T) {
	caller, err := authenticator(t).Authenticate(withCredential("Bearer " + acquirerKey))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if caller.ID != "acquirer-a" {
		t.Errorf("caller = %q, want acquirer-a", caller.ID)
	}
}

func TestCredentialsAreNotInterchangeable(t *testing.T) {
	caller, err := authenticator(t).Authenticate(withCredential("Bearer " + otherKey))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if caller.ID != "acquirer-b" {
		t.Errorf("caller = %q, want acquirer-b", caller.ID)
	}
}

func TestAWrongCredentialIsRefused(t *testing.T) {
	_, err := authenticator(t).Authenticate(withCredential("Bearer not-the-right-key-but-long-enough"))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error = %v, want Unauthenticated", err)
	}
}

func TestEveryRefusalLooksTheSame(t *testing.T) {
	a := authenticator(t)

	_, missing := a.Authenticate(context.Background())
	_, empty := a.Authenticate(withCredential(""))
	_, wrong := a.Authenticate(withCredential("Bearer wrong-key-but-still-long-enough-x"))

	// Distinguishing absent from incorrect hands an attacker a probe.
	for name, err := range map[string]error{"missing": missing, "empty": empty, "wrong": wrong} {
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("%s: error = %v, want Unauthenticated", name, err)
		}
	}
	if status.Convert(missing).Message() != status.Convert(wrong).Message() {
		t.Fatalf("an absent credential is distinguishable from an incorrect one: %q vs %q",
			status.Convert(missing).Message(), status.Convert(wrong).Message())
	}
}

func TestANonBearerSchemeIsRefused(t *testing.T) {
	_, err := authenticator(t).Authenticate(withCredential("Basic " + acquirerKey))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error = %v, want Unauthenticated", err)
	}
}

func TestTheCredentialIsNotEchoedInTheError(t *testing.T) {
	_, err := authenticator(t).Authenticate(withCredential("Bearer " + acquirerKey + "x"))

	if strings.Contains(status.Convert(err).Message(), acquirerKey) {
		t.Fatal("the error message contains the presented credential")
	}
}

func TestAnEmptyCredentialSetIsRefused(t *testing.T) {
	// An edge service that authenticates nobody is open, not degraded.
	if _, err := New(nil); err == nil {
		t.Fatal("New accepted an empty credential set")
	}
}

func TestAShortCredentialIsRefused(t *testing.T) {
	if _, err := New(map[string]string{"acquirer-a": "short"}); err == nil {
		t.Fatal("New accepted a guessable credential")
	}
}

func TestSharedCredentialsAreRefused(t *testing.T) {
	_, err := New(map[string]string{"acquirer-a": acquirerKey, "acquirer-b": acquirerKey})
	if err == nil {
		t.Fatal("New accepted two callers sharing one credential")
	}
}

func TestParseBuildsAnAuthenticator(t *testing.T) {
	a, err := Parse("acquirer-a=" + acquirerKey + ", acquirer-b=" + otherKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	caller, err := a.Authenticate(withCredential("Bearer " + otherKey))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if caller.ID != "acquirer-b" {
		t.Errorf("caller = %q, want acquirer-b", caller.ID)
	}
}

func TestParseRejectsAMalformedEntry(t *testing.T) {
	if _, err := Parse(acquirerKey); err == nil {
		t.Fatal("Parse accepted an entry with no caller")
	}
}
