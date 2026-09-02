package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommended).
const (
	argon2Memory      = 19456 // 19 MiB
	argon2Iterations  = 2
	argon2Parallelism = 1
	argon2KeyLen      = 32
	argon2SaltLen     = 16
)

// PlaceholderHash is a sentinel value for users that have no password (e.g.,
// OAuth-only accounts). It satisfies the NOT NULL constraint on password_hash
// and is never accepted by Verify because its prefix doesn't match "$argon2id$".
const PlaceholderHash = "$not-set$"

// IsUsable reports whether a stored hash could ever verify a password.
//
// It answers what users.password_set cannot. That column is a CLAIM the
// creating flow makes, and two flows set it true with no hash behind it: the
// solo bootstrap stores an empty string, and an account may hold
// PlaceholderHash. Login already works around this -- it verifies and treats a
// parse failure as a wrong password -- so the column alone means "somebody
// intended a password here", not "a password works".
//
// A caller that must know whether the account can SIGN IN asks this. It is the
// cheap half of Verify: the format test, without the Argon2 work, which is
// what makes it usable on a path that has no password to check.
func IsUsable(storedHash string) bool {
	parts := strings.Split(storedHash, "$")
	return len(parts) == 6 && parts[1] == "argon2id"
}

// Hash hashes a password using Argon2id with OWASP-recommended parameters.
// Returns a PHC-formatted string.
func Hash(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory,
		argon2Iterations,
		argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// Verify checks a password against a stored Argon2id hash.
func Verify(storedHash, password string) (bool, error) {
	// Parse PHC format: $argon2id$v=V$m=M,t=T,p=P$salt$hash
	//
	// The shape test is IsUsable, so "a hash Verify can read" has one
	// definition. A caller that only needs to know whether the account can
	// sign in asks that instead, and pays no Argon2 work for the answer.
	if !IsUsable(storedHash) {
		return false, fmt.Errorf("invalid argon2id hash format")
	}
	parts := strings.Split(storedHash, "$")

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("parse argon2id version: %w", err)
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, fmt.Errorf("parse argon2id params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode argon2id salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode argon2id hash: %w", err)
	}

	computedHash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedHash)))

	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1, nil
}
