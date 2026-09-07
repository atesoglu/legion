package pseudonym

import (
	"bytes"
	"strings"
	"testing"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
)

const (
	account = commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_ACCOUNT
	device  = commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_DEVICE
)

func keyring(t *testing.T, version string, fill byte) *Keyring {
	t.Helper()
	k, err := NewKeyring(version, bytes.Repeat([]byte{fill}, MinimumKeyBytes))
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return k
}

func value(t *testing.T, k *Keyring, domain commonv1.IdentifierDomain, source string) string {
	t.Helper()
	id, err := k.Pseudonymise(domain, source)
	if err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}
	return id.GetValue()
}

func TestTheSameInputAlwaysYieldsTheSamePseudonym(t *testing.T) {
	k := keyring(t, "v1", 0x01)

	first := value(t, k, account, "acct-1")
	second := value(t, k, account, "acct-1")

	if first != second {
		t.Fatal("the same identifier produced two different pseudonyms")
	}
}

func TestDomainsCannotBeCrossJoined(t *testing.T) {
	k := keyring(t, "v1", 0x01)

	// The same raw string in two domains must not be relatable.
	if value(t, k, account, "shared-id") == value(t, k, device, "shared-id") {
		t.Fatal("the same identifier produced one pseudonym across two domains")
	}
}

func TestRotatingTheKeyChangesEveryPseudonym(t *testing.T) {
	first := keyring(t, "v1", 0x01)
	second := keyring(t, "v2", 0x02)

	if value(t, first, account, "acct-1") == value(t, second, account, "acct-1") {
		t.Fatal("two keys produced the same pseudonym")
	}
}

func TestTheKeyVersionTravelsWithTheValue(t *testing.T) {
	k := keyring(t, "v7", 0x01)

	id, err := k.Pseudonymise(account, "acct-1")
	if err != nil {
		t.Fatalf("Pseudonymise: %v", err)
	}

	// Without this, a rotation makes every historical pseudonym uninterpretable.
	if id.GetKeyVersion() != "v7" {
		t.Errorf("key_version = %q, want v7", id.GetKeyVersion())
	}
	if id.GetDomain() != account {
		t.Errorf("domain = %v, want account", id.GetDomain())
	}
}

func TestThePseudonymDoesNotContainTheSource(t *testing.T) {
	k := keyring(t, "v1", 0x01)
	const source = "4111111111111111"

	if strings.Contains(value(t, k, account, source), source) {
		t.Fatal("the pseudonym contains its own input")
	}
}

func TestAShortKeyIsRefused(t *testing.T) {
	// A short key silently undoes everything else this package does.
	if _, err := NewKeyring("v1", bytes.Repeat([]byte{1}, MinimumKeyBytes-1)); err == nil {
		t.Fatal("NewKeyring accepted a key below the minimum length")
	}
}

func TestAnUnversionedKeyIsRefused(t *testing.T) {
	if _, err := NewKeyring("", bytes.Repeat([]byte{1}, MinimumKeyBytes)); err == nil {
		t.Fatal("NewKeyring accepted a key with no version")
	}
}

func TestAnUnspecifiedDomainIsRefused(t *testing.T) {
	k := keyring(t, "v1", 0x01)

	_, err := k.Pseudonymise(commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED, "acct-1")
	if err == nil {
		t.Fatal("Pseudonymise accepted an unspecified domain")
	}
}

func TestAnEmptySourceIsRefused(t *testing.T) {
	k := keyring(t, "v1", 0x01)

	if _, err := k.Pseudonymise(account, ""); err == nil {
		t.Fatal("Pseudonymise accepted an empty source identifier")
	}
}

func TestTheKeyIsCopiedNotAliased(t *testing.T) {
	key := bytes.Repeat([]byte{0x01}, MinimumKeyBytes)
	k, err := NewKeyring("v1", key)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	before := value(t, k, account, "acct-1")
	for i := range key {
		key[i] = 0xFF
	}

	if value(t, k, account, "acct-1") != before {
		t.Fatal("mutating the caller's slice changed the keyring")
	}
}
