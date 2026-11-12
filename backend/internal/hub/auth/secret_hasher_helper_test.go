package auth

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/leapmux/leapmux/internal/util/id"
)

// Internal auth tests cannot import hub/testutil, which imports auth.
func testSecretHasher() SecretHasher {
	hasher, err := NewSecretHasher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	return hasher
}

const testSessionSecret = "ssssssssssssssssssssssssssssssssssssssssssssssss"

func testSessionID(label string) string {
	digest := sha256.Sum256([]byte(label))
	return hex.EncodeToString(digest[:])[:id.GeneratedLength]
}

func testSessionToken(label string) string {
	return FormatSessionToken(testSessionID(label), testSessionSecret)
}
