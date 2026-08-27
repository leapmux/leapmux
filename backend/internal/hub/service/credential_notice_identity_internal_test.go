package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestIssuedByAnotherPerson_BlankIdKeepsTheAlarm pins the polarity of the one
// identity comparison in the credential-issued notice.
//
// The comparison picks the WORDING of a security mail: an administrator who
// issues a credential for somebody else must send the alarm, and an
// administrator who issues one for themselves must not. The direction that
// matters is the blank id: an actor the hub cannot identify reads as a third
// party, so an unidentified issuer never silences the alarm.
func TestIssuedByAnotherPerson_BlankIdKeepsTheAlarm(t *testing.T) {
	owner := &store.User{ID: "user-1"}

	t.Run("another administrator sends the alarm", func(t *testing.T) {
		actor := &auth.UserInfo{ID: userid.MustNew("admin-1")}
		assert.True(t, issuedByAnotherPerson(actor, owner))
	})

	t.Run("the owner sends a receipt", func(t *testing.T) {
		actor := &auth.UserInfo{ID: userid.MustNew("user-1")}
		assert.False(t, issuedByAnotherPerson(actor, owner))
	})

	t.Run("a blank actor id keeps the alarm", func(t *testing.T) {
		assert.True(t, issuedByAnotherPerson(&auth.UserInfo{}, owner),
			"an actor the hub cannot identify must never read as the owner")
	})

	t.Run("a blank owner id keeps the alarm", func(t *testing.T) {
		actor := &auth.UserInfo{ID: userid.MustNew("admin-1")}
		assert.True(t, issuedByAnotherPerson(actor, &store.User{}))
	})

	t.Run("a blank id on both sides keeps the alarm", func(t *testing.T) {
		assert.True(t, issuedByAnotherPerson(&auth.UserInfo{}, &store.User{}),
			"two blanks must not compare equal")
	})

	t.Run("a nil on either side keeps the alarm", func(t *testing.T) {
		assert.True(t, issuedByAnotherPerson(nil, owner))
		assert.True(t, issuedByAnotherPerson(&auth.UserInfo{ID: userid.MustNew("admin-1")}, nil))
	})
}
