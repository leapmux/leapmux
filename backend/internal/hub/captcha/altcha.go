package captcha

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

// Algorithm names accepted by AltchaSettings.Validate, mapped to their
// DeriveKey funcs. The memory-hard families (SCRYPT, ARGON2ID) are
// verified server-side the same way; the frontend loads their solver
// workers dynamically (see frontend's lib/altchaSolvers.ts) because the
// stock altcha widget build pre-registers only the SHA and PBKDF2
// families.
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

// SupportedAltchaAlgorithms lists every ALTCHA algorithm the hub can
// issue and verify, sorted. Error messages and the admin CLI derive
// their lists from here so they cannot drift from the deriveKeyFuncs
// registry.
func SupportedAltchaAlgorithms() []string {
	algorithms := make([]string, 0, len(deriveKeyFuncs))
	for name := range deriveKeyFuncs {
		algorithms = append(algorithms, name)
	}
	sort.Strings(algorithms)
	return algorithms
}

// AltchaSettings are the proof-of-work parameters of the built-in
// ALTCHA provider, stored as the altcha row's settings JSON.
//
// Cost semantics note: ALTCHA v2's cost is the per-derivation iteration
// count, and the solver brute-forces ~256 derivations to find a key with
// the default 1-byte "00" prefix — total browser work is therefore
// ~256 x cost iterations. At 10,000 that is ~2.6M PBKDF2-SHA256
// iterations, well under a second in native WebCrypto on desktop and a
// couple of seconds on low-end mobile. Raising cost increases bot cost
// and human wait linearly — there is no ratio gain — so values far above
// this default mostly punish users.
type AltchaSettings struct {
	Algorithm              string `json:"algorithm"`
	Cost                   int64  `json:"cost"`
	MemoryCost             int64  `json:"memory_cost"`
	Parallelism            int64  `json:"parallelism"`
	ChallengeExpirySeconds int64  `json:"challenge_expiry_seconds"`
}

// DefaultAltchaSettings returns the safe out-of-the-box ALTCHA
// parameters.
func DefaultAltchaSettings() AltchaSettings {
	return AltchaSettings{
		Algorithm:              "PBKDF2/SHA-256",
		Cost:                   10000,
		MemoryCost:             0, // 0 = algorithm default
		Parallelism:            0, // 0 = algorithm default
		ChallengeExpirySeconds: 1200,
	}
}

// DefaultAltchaSettingsFor returns the recommended parameters for one
// algorithm family. A zero Cost/MemoryCost/Parallelism means "the
// family's library default" — the altcha derive funcs substitute their
// own defaults for zero values, and these constants match those
// substitutions.
//
// The admin CLI applies this on an algorithm switch so parameters from
// the old family are never reinterpreted in the new family's units
// (SCRYPT's r is a block-count multiplier, not KiB; ARGON2ID's m is KiB).
func DefaultAltchaSettingsFor(algorithm string) (AltchaSettings, error) {
	if _, ok := deriveKeyFuncs[algorithm]; !ok {
		return AltchaSettings{}, fmt.Errorf("unsupported captcha algorithm %q (supported: %s)", algorithm, strings.Join(SupportedAltchaAlgorithms(), ", "))
	}
	s := DefaultAltchaSettings()
	s.Algorithm = algorithm
	switch algorithm {
	case "SCRYPT":
		// 128 * N * r = 16 MiB per derivation.
		s.Cost, s.MemoryCost, s.Parallelism = 16384, 8, 1
	case "ARGON2ID":
		// MemoryCost is KiB: 64 MiB per derivation.
		s.Cost, s.MemoryCost, s.Parallelism = 1, 65536, 1
	default:
		// SHA and PBKDF2 families use Cost only (iterations); the
		// shared default of 10000 is the recommended value.
	}
	return s, nil
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

// Validate rejects ALTCHA settings that could break login for every
// user: unknown algorithms, parameters the KDF libraries refuse (SCRYPT
// requires a power-of-two N), costs that would stall or OOM browsers,
// family-foreign parameters, or expiry windows outside a sane range.
func (s AltchaSettings) Validate() error {
	if _, ok := deriveKeyFuncs[s.Algorithm]; !ok {
		return fmt.Errorf("unsupported captcha algorithm %q (supported: %s)", s.Algorithm, strings.Join(SupportedAltchaAlgorithms(), ", "))
	}
	// The shared Cost/MemoryCost/Parallelism fields carry a different unit
	// per family, so every range is family-specific and parameters that the
	// family does not use must be zero: a nonzero leftover (an ARGON2ID
	// memory carried into SCRYPT, where it becomes the block multiplier r)
	// silently changes meaning across the switch.
	switch s.Algorithm {
	case "SCRYPT":
		if s.Cost < 1024 || s.Cost > 1048576 || s.Cost&(s.Cost-1) != 0 {
			return fmt.Errorf("captcha cost (SCRYPT N) must be a power of two between 1024 and 1048576 (got %d)", s.Cost)
		}
		if s.MemoryCost < 1 || s.MemoryCost > 32 {
			return fmt.Errorf("captcha memory_cost (SCRYPT r, a block-count multiplier - NOT KiB) must be between 1 and 32 (got %d)", s.MemoryCost)
		}
		if s.Parallelism < 1 || s.Parallelism > 8 {
			return fmt.Errorf("captcha parallelism (SCRYPT p) must be between 1 and 8 (got %d)", s.Parallelism)
		}
		if mem := 128 * s.Cost * s.MemoryCost; mem > maxScryptMemoryBytes {
			return fmt.Errorf("captcha SCRYPT memory (128 * N * r) must be at most %d bytes (got %d)", maxScryptMemoryBytes, mem)
		}
	case "ARGON2ID":
		if s.Cost < 1 || s.Cost > 64 {
			return fmt.Errorf("captcha cost (ARGON2ID time parameter) must be between 1 and 64 (got %d)", s.Cost)
		}
		if s.MemoryCost < 8192 || s.MemoryCost > maxArgon2idMemoryKiB {
			return fmt.Errorf("captcha memory_cost (ARGON2ID m, in KiB) must be between 8192 and %d (got %d)", maxArgon2idMemoryKiB, s.MemoryCost)
		}
		if s.Parallelism < 1 || s.Parallelism > 4 {
			return fmt.Errorf("captcha parallelism (ARGON2ID threads) must be between 1 and 4 (got %d)", s.Parallelism)
		}
	case "PBKDF2/SHA-256", "PBKDF2/SHA-384", "PBKDF2/SHA-512":
		if s.Cost < 10000 || s.Cost > 1000000 {
			return fmt.Errorf("captcha cost (PBKDF2 iterations) must be between 10000 and 1000000 (got %d)", s.Cost)
		}
		if s.MemoryCost != 0 || s.Parallelism != 0 {
			return fmt.Errorf("captcha memory_cost and parallelism must be 0 for PBKDF2 (got %d, %d)", s.MemoryCost, s.Parallelism)
		}
	default: // SHA-256, SHA-384, SHA-512
		if s.Cost < 1000 || s.Cost > 1000000 {
			return fmt.Errorf("captcha cost (SHA iterations) must be between 1000 and 1000000 (got %d)", s.Cost)
		}
		if s.MemoryCost != 0 || s.Parallelism != 0 {
			return fmt.Errorf("captcha memory_cost and parallelism must be 0 for SHA algorithms (got %d, %d)", s.MemoryCost, s.Parallelism)
		}
	}
	if s.ChallengeExpirySeconds < 60 || s.ChallengeExpirySeconds > 86400 {
		return fmt.Errorf("captcha challenge expiry must be between 60s and 86400s (got %ds)", s.ChallengeExpirySeconds)
	}
	return nil
}

// parseAltchaSettings decodes a stored settings JSON over the family
// defaults: missing or zero fields take the algorithm family's values
// (matching what the derive funcs substitute for zeros), and an
// undecodable blob yields the built-in defaults. Validation happens in
// Effective, which falls back to DefaultConfig on refusal.
func parseAltchaSettings(raw string) AltchaSettings {
	if raw == "" {
		return DefaultAltchaSettings()
	}
	var stored AltchaSettings
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return DefaultAltchaSettings()
	}
	family := DefaultAltchaSettings()
	if stored.Algorithm != "" {
		f, err := DefaultAltchaSettingsFor(stored.Algorithm)
		if err != nil {
			return DefaultAltchaSettings()
		}
		family = f
	}
	if stored.Cost != 0 {
		family.Cost = stored.Cost
	}
	if stored.MemoryCost != 0 {
		family.MemoryCost = stored.MemoryCost
	}
	if stored.Parallelism != 0 {
		family.Parallelism = stored.Parallelism
	}
	if stored.ChallengeExpirySeconds != 0 {
		family.ChallengeExpirySeconds = stored.ChallengeExpirySeconds
	}
	return family
}

// decodeAltchaPayload accepts both padded and unpadded standard-alphabet
// base64, the two encodings the widget historically produced across
// versions.
func decodeAltchaPayload(payload string, out *altcha.Payload) error {
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(b, out)
}
