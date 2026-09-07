// Package pseudonym converts persistent real-world identifiers into keyed,
// namespaced, versioned pseudonyms.
//
// Three properties are deliberate and each is enforced here rather than left to
// callers (see docs/domain-model.md §4):
//
//   - Keyed, not plain. An unsalted digest of a card token or an IBAN is
//     reversible by enumeration, because the input domain is small. A keyed
//     construction is not.
//   - Namespaced. The domain is part of the HMAC input, so the same raw string
//     in two domains yields two pseudonyms and cannot be cross-joined by
//     accident.
//   - Versioned. The key version travels with the value, so keys can rotate
//     without losing the ability to interpret historical pseudonyms.
//
// Pseudonymisation reduces exposure. It does not make the data non-personal and
// does not by itself discharge any data protection obligation.
package pseudonym

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
)

// MinimumKeyBytes is the shortest key accepted. HMAC-SHA-256's security rests
// on the key, and a short key is the one mistake that silently undoes
// everything else in this package.
const MinimumKeyBytes = 32

// Keyring holds the platform key currently used to produce pseudonyms.
type Keyring struct {
	version string
	key     []byte
}

// NewKeyring validates and holds a pseudonymisation key.
//
// There is deliberately no default key. A hardcoded fallback would mean every
// deployment that forgot to configure one shared the same pseudonyms, which is
// indistinguishable from not pseudonymising at all.
func NewKeyring(version string, key []byte) (*Keyring, error) {
	if version == "" {
		return nil, fmt.Errorf("pseudonym: key version must not be empty")
	}
	if len(key) < MinimumKeyBytes {
		return nil, fmt.Errorf(
			"pseudonym: key must be at least %d bytes, got %d", MinimumKeyBytes, len(key))
	}

	held := make([]byte, len(key))
	copy(held, key)
	return &Keyring{version: version, key: held}, nil
}

// Version reports the key version stamped onto every pseudonym produced.
func (k *Keyring) Version() string {
	return k.version
}

// Pseudonymise converts one source identifier within one domain.
//
// An unspecified domain is rejected: the domain is what stops the same string
// in two contexts from being joined, so accepting an absent one would silently
// remove the separation this package exists to provide.
func (k *Keyring) Pseudonymise(
	domain commonv1.IdentifierDomain,
	source string,
) (*commonv1.PseudonymousId, error) {
	if domain == commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_UNSPECIFIED {
		return nil, fmt.Errorf("pseudonym: identifier domain must be specified")
	}
	if source == "" {
		return nil, fmt.Errorf("pseudonym: source identifier must not be empty")
	}

	mac := hmac.New(sha256.New, k.key)
	// The separator prevents a domain and source pair from colliding with a
	// different pair that happens to concatenate to the same bytes.
	mac.Write([]byte(domain.String()))
	mac.Write([]byte{0})
	mac.Write([]byte(source))

	return &commonv1.PseudonymousId{
		Value:      hex.EncodeToString(mac.Sum(nil)),
		KeyVersion: k.version,
		Domain:     domain,
	}, nil
}
