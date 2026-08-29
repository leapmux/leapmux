package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// The wire's `verified` field carries the ONE rule every surface reads: a
// vouch or a built-in of this build. The registrations panel used to derive
// it from verified_at, which is false for a built-in the consent page
// already treats as verified -- the same app read two ways on one hub.
func TestAppToProtoVerifiedCarriesTheOneRule(t *testing.T) {
	assert.True(t, appToProto(&store.OAuthClient{
		RegistrationSource: store.OAuthClientSourceBuiltin,
	}, 0, "").Verified, "a built-in is verified by construction")
	assert.Nil(t, appToProto(&store.OAuthClient{
		RegistrationSource: store.OAuthClientSourceBuiltin,
	}, 0, "").VerifiedAt, "with no vouch recorded, the timestamp stays unset")

	assert.False(t, appToProto(&store.OAuthClient{
		RegistrationSource: store.OAuthClientSourceUser,
	}, 0, "").Verified, "a self-registered app starts unverified")

	now := time.Now().UTC()
	assert.True(t, appToProto(&store.OAuthClient{
		RegistrationSource: store.OAuthClientSourceUser,
		VerifiedAt:         &now,
	}, 0, "").Verified, "an administrator's vouch verifies")
}
