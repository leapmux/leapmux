package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// TestPostTouchPollOAuthError_ApprovalNamingNoUserIsNotUsable pins the poll
// guard that refuses an approved device grant whose user_id is blank.
//
// The store now refuses to WRITE that row -- Approve/ApproveByUserCode reject an
// unminted approver in all three dialects -- so this state can no longer be
// reached through the store API, which is why the assertion lives here on the
// decision function rather than end-to-end through the token endpoint. It is
// still worth having: the guard's job is corrupt data (a row predating the write
// guard, a restored backup, a manual edit), and a row is a column's contents,
// not a program invariant. Without it, a blank user_id would reach the mint and
// issue a token that authenticates as nobody.
//
// The 0/2 cases are the ordinary ones, asserted alongside so a refactor that
// reorders the switch cannot quietly turn "denied" into "pending".
func TestPostTouchPollOAuthError_ApprovalNamingNoUserIsNotUsable(t *testing.T) {
	h := &APIAuthHandler{}

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
		"approved but naming no user": {
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
			code, _, stop := h.postTouchPollOAuthError(&tc.row)
			assert.Equal(t, tc.wantStop, stop)
			assert.Equal(t, tc.wantCode, code)
		})
	}
}
