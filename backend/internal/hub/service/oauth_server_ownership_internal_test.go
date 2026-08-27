package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestAssertAppOwner_ZeroCallerOwnsNothing pins the zero-id denial of the app
// edit predicate.
//
// The comparison is `user.ID.Matches(app.OwnerUserID)`, and the polarity is a
// GRANT: false means deny. A zero caller id and a blank stored owner must not
// read as the same person -- and a blank stored owner is not even a real state
// here, because a hub-wide app stores SQL NULL, which loads as "". So the
// blank-vs-blank case is precisely the one that would go wrong.
func TestAssertAppOwner_ZeroCallerOwnsNothing(t *testing.T) {
	t.Parallel()

	owned := &store.OAuthClient{ClientID: "c1", OwnerUserID: "u-alice"}
	hubWide := &store.OAuthClient{ClientID: "c2"}

	assert.False(t, assertAppOwner(owned, nil), "no caller owns nothing")
	assert.False(t, assertAppOwner(hubWide, nil))

	zero := &auth.UserInfo{}
	assert.False(t, assertAppOwner(owned, zero), "a zero caller does not own another user's app")
	assert.False(t, assertAppOwner(hubWide, zero),
		"a zero caller must not match a hub-wide app's blank owner; only an administrator edits one")

	zeroAdmin := &auth.UserInfo{IsAdmin: true}
	assert.True(t, assertAppOwner(hubWide, zeroAdmin),
		"the hub-wide arm is decided by IsAdmin, which is an ACCOUNT fact rather than an identity comparison")
	assert.False(t, assertAppOwner(owned, zeroAdmin),
		"an administrator does not own another user's PRIVATE app; the app list shows them separately")

	alice := &auth.UserInfo{ID: userid.MustNew("u-alice")}
	assert.True(t, assertAppOwner(owned, alice))
	assert.False(t, assertAppOwner(hubWide, alice), "editing a hub-wide app needs an administrator")
}
