package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestPostTouchPollOAuthError_ApprovalWithNoUserIsNotUsable pins the poll
// guard that refuses an approved device grant whose user_id is blank.
//
// The store now refuses to WRITE that row -- Approve/ApproveByUserCode reject an
// unminted approver in all three dialects -- so no caller can reach that state
// through the store API, which is why the assertion lives here on the
// decision function rather than end-to-end through the token endpoint. It is
// still worth having: the guard's job is corrupt data (a row predating the write
// guard, a restored backup, a manual edit), and a row is a column's contents,
// not a program invariant. Without it, a blank user_id would reach the mint and
// issue a token that authenticates as nobody.
//
// The 0/2 cases are the ordinary ones, asserted alongside so a refactor that
// reorders the switch cannot quietly turn "denied" into "pending".
func TestPostTouchPollOAuthError_ApprovalWithNoUserIsNotUsable(t *testing.T) {
	t.Parallel()

	h := &OAuthServerHandler{}

	for name, tc := range map[string]struct {
		row      store.DeviceAuthorization
		wantCode string
		wantStop bool
	}{
		"pending": {
			row:      store.DeviceAuthorization{Approved: 0, UserID: ""},
			wantCode: "authorization_pending",
			wantStop: true,
		},
		"denied": {
			row:      store.DeviceAuthorization{Approved: 2, UserID: "user-1"},
			wantCode: "access_denied",
			wantStop: true,
		},
		"approved but with no user": {
			row:      store.DeviceAuthorization{Approved: 1, UserID: ""},
			wantCode: "authorization_pending",
			wantStop: true,
		},
		"approved by a real user": {
			row:      store.DeviceAuthorization{Approved: 1, UserID: "user-1"},
			wantCode: "",
			wantStop: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, stop := h.postTouchPollOAuthError(&tc.row)
			assert.Equal(t, tc.wantStop, stop)
			assert.Equal(t, tc.wantCode, body.Error)
		})
	}
}

// TestNormalizeDeviceName pins the intake guard on the one attacker-chosen
// string that reaches a security notice.
//
// Whoever runs the CLI writes the device name, and the device-code leg
// that accepts it is ANONYMOUS. It then reaches the consent page, the
// activation page, the account's CLI-credential list, the stored row, and
// the plain-text email that tells an owner a credential was issued. A
// newline writes arbitrary lines into that notice -- including a second
// signature delimiter and a forged hub address -- so whoever created the
// credential could also write the one signal the docs call "how you learn
// about a credential you did not create".
func TestNormalizeDeviceName(t *testing.T) {
	t.Parallel()

	t.Run("a newline cannot forge lines in the issuance notice", func(t *testing.T) {
		t.Parallel()
		got := normalizeInstallationName("laptop\n\n-- \nThis is an automated message from your LeapMux hub at http://evil.test.")
		assert.NotContains(t, got, "\n", "a control character must not survive intake")
		assert.NotContains(t, got, "\r")
	})

	for name, tc := range map[string]struct{ in, want string }{
		"ordinary name":        {"alice@laptop", "alice@laptop"},
		"carriage return":      {"a\rb", "a b"},
		"tab":                  {"a\tb", "a b"},
		"whitespace run":       {"a   \n  b", "a b"},
		"surrounding space":    {"  laptop  ", "laptop"},
		"empty":                {"", ""},
		"only control bytes":   {"\n\r\t", ""},
		"invisible format":     {"lap\u200btop", "laptop"},
		"punctuation survives": {`a"b\c$d%e`, `a"b\c$d%e`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, normalizeInstallationName(tc.in))
		})
	}

	// The narrowest column that holds a device name is MySQL's
	// VARCHAR(255), which counts characters -- so a byte cap can never
	// overflow it. Without the cap the same anonymous POST succeeded on
	// SQLite and Postgres, whose columns are TEXT, and failed the INSERT
	// with a 500 on MySQL.
	t.Run("a long name is cut on a rune boundary", func(t *testing.T) {
		t.Parallel()
		got := normalizeInstallationName(strings.Repeat("한", 500))
		assert.LessOrEqual(t, len(got), installationNameByteLimit)
		assert.True(t, utf8.ValidString(got), "the cut must not split a rune")
		assert.NotEmpty(t, got, "a long name is cut, not discarded")
	})
}
