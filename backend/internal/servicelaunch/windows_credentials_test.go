package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"unicode/utf16"

	ole "github.com/go-ole/go-ole"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCredentialRepairAndFailures checks the native refusal classes so
// the CLI can explain the account or password change that each requires.
func TestCredentialRepairAndFailures(t *testing.T) {
	assert.Contains(t, credentialErrorClass(errOfCode(1326)), "wrong")
	assert.Contains(t, credentialErrorClass(fmt.Errorf("native: %w", syscall.Errno(1330))), "expired")
	assert.Contains(t, credentialErrorClass(errOfCode(1907)), "changed")
	assert.Contains(t, credentialErrorClass(errOfCode(1385)), "logon right")
	assert.Contains(t, credentialErrorClass(errOfCode(1331)), "disabled")
	assert.Contains(t, credentialErrorClass(assert.AnError), "refused")
}

func TestAccountPasswordIsClearedAfterEveryOutcome(t *testing.T) {
	for _, failure := range []error{nil, assert.AnError} {
		password := []byte("private-password")
		err := withAccountPassword(t.Context(), "account", func(context.Context, string) ([]byte, error) { return password, nil }, func(value []byte) error {
			assert.Equal(t, "private-password", string(value))
			return failure
		})
		assert.True(t, errors.Is(err, failure))
		assert.Equal(t, make([]byte, len(password)), password)
	}
	password := []byte("partial-password")
	err := withAccountPassword(t.Context(), "account", func(context.Context, string) ([]byte, error) { return password, assert.AnError }, func([]byte) error { t.Fatal("a provider failure must prevent native setup"); return nil })
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, make([]byte, len(password)), password)
}

// errOfCode carries a windows error code.
type codedError struct{ code uintptr }

func (e codedError) ErrorCode() uintptr { return e.code }
func (codedError) Error() string        { return "coded" }

func errOfCode(code uintptr) error { return codedError{code: code} }

func TestStoredPasswordEncoding(t *testing.T) {
	for _, value := range []string{"password", "a Unicode password: \U0001f511"} {
		encoded := utf16.Encode([]rune(value))
		var blob []byte
		for _, character := range encoded {
			blob = append(blob, byte(character), byte(character>>8))
		}
		decoded, err := decodeStoredPassword(blob)
		require.NoError(t, err)
		assert.Equal(t, value, string(decoded))
		clear(decoded)
	}
	for _, value := range [][]byte{nil, {1}, make([]byte, 2562), {0, 0}, {0, 0xd8}, {0, 0xdc}, {0, 0xd8, 0x61, 0}} {
		_, err := decodeStoredPassword(value)
		require.Error(t, err)
	}
}

func TestCredentialCancellationDoesNotReadPassword(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := withAccountPassword(ctx, "account", func(context.Context, string) ([]byte, error) {
		called = true
		return []byte("private-password"), nil
	}, func([]byte) error { t.Fatal("cancellation must prevent native configuration"); return nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)
}

func TestWindowsCallFailurePreservesNativeErrors(t *testing.T) {
	for _, err := range []error{nil, syscall.Errno(0)} {
		require.ErrorIs(t, windowsCallFailure(err), syscall.Errno(31))
	}
	require.ErrorIs(t, windowsCallFailure(assert.AnError), assert.AnError)
}

func TestAccountPasswordRequiresNonemptyValue(t *testing.T) {
	for _, password := range [][]byte{nil, {}} {
		err := withAccountPassword(t.Context(), "account", func(context.Context, string) ([]byte, error) { return password, nil }, func([]byte) error { t.Fatal("an absent password must prevent native setup"); return nil })
		require.ErrorContains(t, err, "password is required")
	}
}

func TestWindowsLogonName(t *testing.T) {
	for _, test := range []struct{ input, username, domain string }{
		{`DOMAIN\user`, "user", "DOMAIN"},
		{`.\user`, "user", "."},
		{"user@example.com", "user@example.com", ""},
		{"local user", "local user", "."},
	} {
		username, domain, err := windowsLogonName(test.input)
		require.NoError(t, err)
		assert.Equal(t, test.username, username)
		assert.Equal(t, test.domain, domain)
	}
	for _, input := range []string{"", `\user`, `DOMAIN\`, `DOMAIN\user\extra`, "invalid\x00user"} {
		_, _, err := windowsLogonName(input)
		require.Error(t, err)
	}
}

type automationException struct{ code uint32 }

func (e automationException) Error() string { return "private native description" }
func (e automationException) SCODE() uint32 { return e.code }

func TestCredentialErrorCodeReadsAutomationException(t *testing.T) {
	for _, test := range []struct {
		inner    uint32
		expected uint32
	}{
		{0x80070002, 2}, {0x80070005, 5}, {0x80070569, 1385},
		{0, 0x80020009}, {5, 0x80020009}, {0x80004005, 0x80004005},
	} {
		err := ole.NewErrorWithSubError(0x80020009, "private-password", automationException{code: test.inner})
		assert.Equal(t, test.expected, credentialErrorCode(fmt.Errorf("invoke: %w", err)))
	}
	assert.Equal(t, uint32(0x80020009), credentialErrorCode(ole.NewErrorWithSubError(0x80020009, "private-password", nil)))
}
