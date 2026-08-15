// Package captcha implements the hub's ALTCHA v2 proof-of-work bot
// protection: challenge issuance, solution verification with single-use
// enforcement, and the ConnectRPC interceptor that controls the
// unauthenticated credential procedures (Login, SignUp, CompleteOAuthSignup).
package captcha

import (
	"fmt"
	"sort"
	"strings"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// Algorithm names accepted by Validate, mapped to their DeriveKey funcs.
// The memory-hard families (SCRYPT, ARGON2ID) are verified server-side the
// same way; the frontend loads their solver workers dynamically (see
// frontend's lib/altchaSolvers.ts) because the stock altcha widget build
// pre-registers only the SHA and PBKDF2 families.
var deriveKeyFuncs = map[string]altcha.DeriveKeyFunc{
	"SHA-256":        altcha.DeriveKeySHA(),
	"SHA-384":        altcha.DeriveKeySHA(),
	"SHA-512":        altcha.DeriveKeySHA(),
	"PBKDF2/SHA-256": altcha.DeriveKeyPBKDF2(),
	"PBKDF2/SHA-384": altcha.DeriveKeyPBKDF2(),
	"PBKDF2/SHA-512": altcha.DeriveKeyPBKDF2(),
	"SCRYPT":         altcha.DeriveKeyScrypt(),
	"ARGON2ID":       altcha.DeriveKeyArgon2id(),
}

// SupportedAlgorithms lists every algorithm the hub can issue and verify,
// sorted. Error messages and the admin CLI derive their lists from here so
// they cannot drift from the deriveKeyFuncs registry.
func SupportedAlgorithms() []string {
	algorithms := make([]string, 0, len(deriveKeyFuncs))
	for name := range deriveKeyFuncs {
		algorithms = append(algorithms, name)
	}
	sort.Strings(algorithms)
	return algorithms
}

// Config is the effective captcha configuration. It is built by overlaying
// a stored captcha_config row (if any) onto DefaultConfig.
type Config struct {
	Enabled                bool   `json:"enabled"`
	Algorithm              string `json:"algorithm"`
	Cost                   int64  `json:"cost"`
	MemoryCost             int64  `json:"memory_cost"`
	Parallelism            int64  `json:"parallelism"`
	ChallengeExpirySeconds int64  `json:"challenge_expiry_seconds"`
}

// DefaultConfig returns the safe out-of-the-box configuration.
//
// Cost semantics note: ALTCHA v2's cost is the per-derivation iteration
// count, and the solver brute-forces ~256 derivations to find a key with
// the default 1-byte "00" prefix — total browser work is therefore
// ~256 × cost iterations. At 10,000 that is ≈2.6M PBKDF2-SHA256
// iterations, well under a second in native WebCrypto on desktop and a
// couple of seconds on low-end mobile. Raising cost increases bot cost and
// human wait linearly — there is no ratio gain — so values far above this
// default mostly punish users.
func DefaultConfig() Config {
	return Config{
		Enabled:                true,
		Algorithm:              "PBKDF2/SHA-256",
		Cost:                   10000,
		MemoryCost:             0, // 0 = algorithm default
		Parallelism:            0, // 0 = algorithm default
		ChallengeExpirySeconds: 1200,
	}
}

// FamilyDefaults returns the recommended parameters for one algorithm
// family, with Enabled copied from the built-in default. A zero
// Cost/MemoryCost/Parallelism means "the family's library default" — the
// altcha derive funcs substitute their own defaults for zero values, and
// these constants match those substitutions.
//
// The admin CLI applies this on an algorithm switch so parameters from the
// old family are never reinterpreted in the new family's units (SCRYPT's
// r is a block-count multiplier, not KiB; ARGON2ID's m is KiB).
func FamilyDefaults(algorithm string) (Config, error) {
	if _, ok := deriveKeyFuncs[algorithm]; !ok {
		return Config{}, fmt.Errorf("unsupported captcha algorithm %q (supported: %s)", algorithm, strings.Join(SupportedAlgorithms(), ", "))
	}
	cfg := DefaultConfig()
	cfg.Algorithm = algorithm
	switch algorithm {
	case "SCRYPT":
		// 128 * N * r = 16 MiB per derivation.
		cfg.Cost, cfg.MemoryCost, cfg.Parallelism = 16384, 8, 1
	case "ARGON2ID":
		// MemoryCost is KiB: 64 MiB per derivation.
		cfg.Cost, cfg.MemoryCost, cfg.Parallelism = 1, 65536, 1
	default:
		// SHA and PBKDF2 families use Cost only (iterations); the
		// shared default of 10000 is the recommended value.
	}
	return cfg, nil
}

// Derivation memory limits for the memory-hard families. Both the browser
// worker and the hub's server-side re-derivation allocate the full
// per-derivation memory on every solve and every Verify call, and Verify
// runs on unauthenticated procedures, so the ceilings stay at sizes a
// low-end browser tab survives.
const (
	// maxScryptMemoryBytes limits 128 * N * r.
	maxScryptMemoryBytes = 64 << 20 // 64 MiB
	// maxArgon2idMemoryKiB limits ARGON2ID's m.
	maxArgon2idMemoryKiB = 131072 // 128 MiB
)

// Validate rejects configurations that could break login for every user:
// unknown algorithms, parameters the KDF libraries refuse (SCRYPT requires
// a power-of-two N), costs that would stall or OOM browsers, family-foreign
// parameters, or expiry windows outside a sane range.
func (c Config) Validate() error {
	if _, ok := deriveKeyFuncs[c.Algorithm]; !ok {
		return fmt.Errorf("unsupported captcha algorithm %q (supported: %s)", c.Algorithm, strings.Join(SupportedAlgorithms(), ", "))
	}
	// The shared Cost/MemoryCost/Parallelism fields carry a different unit
	// per family, so every range is family-specific and parameters that the
	// family does not use must be zero: a nonzero leftover (an ARGON2ID
	// memory carried into SCRYPT, where it becomes the block multiplier r)
	// silently changes meaning across the switch.
	switch c.Algorithm {
	case "SCRYPT":
		if c.Cost < 1024 || c.Cost > 1048576 || c.Cost&(c.Cost-1) != 0 {
			return fmt.Errorf("captcha cost (SCRYPT N) must be a power of two between 1024 and 1048576 (got %d)", c.Cost)
		}
		if c.MemoryCost < 1 || c.MemoryCost > 32 {
			return fmt.Errorf("captcha memory_cost (SCRYPT r, a block-count multiplier — NOT KiB) must be between 1 and 32 (got %d)", c.MemoryCost)
		}
		if c.Parallelism < 1 || c.Parallelism > 8 {
			return fmt.Errorf("captcha parallelism (SCRYPT p) must be between 1 and 8 (got %d)", c.Parallelism)
		}
		if mem := 128 * c.Cost * c.MemoryCost; mem > maxScryptMemoryBytes {
			return fmt.Errorf("captcha SCRYPT memory (128 * N * r) must be at most %d bytes (got %d)", maxScryptMemoryBytes, mem)
		}
	case "ARGON2ID":
		if c.Cost < 1 || c.Cost > 64 {
			return fmt.Errorf("captcha cost (ARGON2ID time parameter) must be between 1 and 64 (got %d)", c.Cost)
		}
		if c.MemoryCost < 8192 || c.MemoryCost > maxArgon2idMemoryKiB {
			return fmt.Errorf("captcha memory_cost (ARGON2ID m, in KiB) must be between 8192 and %d (got %d)", maxArgon2idMemoryKiB, c.MemoryCost)
		}
		if c.Parallelism < 1 || c.Parallelism > 4 {
			return fmt.Errorf("captcha parallelism (ARGON2ID threads) must be between 1 and 4 (got %d)", c.Parallelism)
		}
	case "PBKDF2/SHA-256", "PBKDF2/SHA-384", "PBKDF2/SHA-512":
		if c.Cost < 10000 || c.Cost > 1000000 {
			return fmt.Errorf("captcha cost (PBKDF2 iterations) must be between 10000 and 1000000 (got %d)", c.Cost)
		}
		if c.MemoryCost != 0 || c.Parallelism != 0 {
			return fmt.Errorf("captcha memory_cost and parallelism must be 0 for PBKDF2 (got %d, %d)", c.MemoryCost, c.Parallelism)
		}
	default: // SHA-256, SHA-384, SHA-512
		if c.Cost < 1000 || c.Cost > 1000000 {
			return fmt.Errorf("captcha cost (SHA iterations) must be between 1000 and 1000000 (got %d)", c.Cost)
		}
		if c.MemoryCost != 0 || c.Parallelism != 0 {
			return fmt.Errorf("captcha memory_cost and parallelism must be 0 for SHA algorithms (got %d, %d)", c.MemoryCost, c.Parallelism)
		}
	}
	if c.ChallengeExpirySeconds < 60 || c.ChallengeExpirySeconds > 86400 {
		return fmt.Errorf("captcha challenge expiry must be between 60s and 86400s (got %ds)", c.ChallengeExpirySeconds)
	}
	return nil
}
