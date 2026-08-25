package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
)

// classifyWebAuthnError is the ONE place that knows the hubwebauthn
// sentinels, and four surfaces answer from it. A sentinel that falls to the
// default is a 500 for ordinary user input -- which is exactly what the
// passkey-management surface returned for a cancelled platform prompt
// before this classifier existed.
//
// The wrapped cases matter as much as the bare ones: every sentinel reaches
// a handler through at least one fmt.Errorf, and the store layer's
// transaction wrapper adds another.
func TestClassifyWebAuthnError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want webAuthnErrorClass
	}{
		{"clone warning", hubwebauthn.ErrCloneDetected, webAuthnErrorClone},
		{"invalid ceremony", hubwebauthn.ErrCeremonyInvalid, webAuthnErrorCredential},
		{"rejected assertion", hubwebauthn.ErrAssertionRejected, webAuthnErrorCredential},
		{"no passkeys", hubwebauthn.ErrNoPasskeys, webAuthnErrorUnavailable},
		{"origin not allowed", hubwebauthn.ErrOriginNotAllowed, webAuthnErrorUnavailable},
		{"passkeys unavailable", ErrPasskeysUnavailable, webAuthnErrorUnavailable},
		{"nil", nil, webAuthnErrorInfrastructure},
		{"unknown", fmt.Errorf("database is unwell"), webAuthnErrorInfrastructure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, classifyWebAuthnError(tc.err))
			if tc.err == nil {
				return
			}
			// Wrapped once, as a handler sees it.
			assert.Equal(t, tc.want, classifyWebAuthnError(fmt.Errorf("finish registration: %w", tc.err)),
				"a wrapped sentinel must classify the same")
			// Wrapped twice, as the transaction wrapper leaves it.
			assert.Equal(t, tc.want, classifyWebAuthnError(fmt.Errorf("tx: %w", fmt.Errorf("finish: %w", tc.err))),
				"nesting must not change the class")
		})
	}
}

// Every sentinel the webauthn package exports must be classified. A new one
// that nobody adds to classifyWebAuthnError falls to Internal on all four
// surfaces, which is the silent 500 the classifier exists to prevent -- and
// no compiler check catches it, because a sentinel is just a package-level
// var.
//
// The list below is hand-maintained, so the test reads the SOURCE to prove
// the list is complete. Otherwise it would assert only what someone
// remembered to add, which is the failure it exists to catch.
func TestClassifyWebAuthnError_CoversEveryExportedSentinel(t *testing.T) {
	t.Parallel()

	classified := map[string]error{
		"ErrCloneDetected":     hubwebauthn.ErrCloneDetected,
		"ErrCeremonyInvalid":   hubwebauthn.ErrCeremonyInvalid,
		"ErrAssertionRejected": hubwebauthn.ErrAssertionRejected,
		"ErrNoPasskeys":        hubwebauthn.ErrNoPasskeys,
		"ErrOriginNotAllowed":  hubwebauthn.ErrOriginNotAllowed,
	}

	for name, err := range classified {
		assert.NotEqualf(t, webAuthnErrorInfrastructure, classifyWebAuthnError(err),
			"%s falls to the infrastructure default, so every surface answers it with a 500", name)
	}

	// The declarations the package actually exports.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "webauthn", "service.go"))
	require.NoError(t, err, "the sentinel declarations must stay findable")

	declared := regexp.MustCompile(`(?m)^var (Err\w+) = errors\.New\(`).FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, declared, "the scan found no sentinels, so it proves nothing")
	for _, m := range declared {
		assert.Containsf(t, classified, m[1],
			"webauthn.%s is exported but not classified: it would answer CodeInternal on every passkey surface", m[1])
	}
}
