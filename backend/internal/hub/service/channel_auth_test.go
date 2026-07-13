package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
)

func TestUserCanUseChannelRequiresMatchingCredential(t *testing.T) {
	tests := []struct {
		name        string
		user        *auth.UserInfo
		channelAuth channelmgr.AuthInfo
		want        bool
	}{
		{
			name:        "matching session",
			user:        &auth.UserInfo{ID: "user", Credential: auth.SessionCredential("session-1")},
			channelAuth: channelmgr.AuthInfo{Credential: auth.SessionCredential("session-1")},
			want:        true,
		},
		{
			name:        "different session",
			user:        &auth.UserInfo{ID: "user", Credential: auth.SessionCredential("session-2")},
			channelAuth: channelmgr.AuthInfo{Credential: auth.SessionCredential("session-1")},
		},
		{
			name: "matching API token",
			user: &auth.UserInfo{
				ID: "user", Credential: auth.APICredential("api-1"),
			},
			channelAuth: channelmgr.AuthInfo{
				Credential: auth.APICredential("api-1"),
			},
			want: true,
		},
		{
			name: "different API token",
			user: &auth.UserInfo{
				ID: "user", Credential: auth.APICredential("api-2"),
			},
			channelAuth: channelmgr.AuthInfo{
				Credential: auth.APICredential("api-1"),
			},
		},
		{
			name: "credential type mismatch",
			user: &auth.UserInfo{
				ID: "user", Credential: auth.APICredential("api-1"),
			},
			channelAuth: channelmgr.AuthInfo{Credential: auth.SessionCredential("session-1")},
		},
		{
			name:        "matching credentialless solo user",
			user:        &auth.UserInfo{ID: "user"},
			channelAuth: channelmgr.AuthInfo{},
			want:        true,
		},
		{
			name:        "credentialless user cannot use session channel",
			user:        &auth.UserInfo{ID: "user"},
			channelAuth: channelmgr.AuthInfo{Credential: auth.SessionCredential("session-1")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, userCanUseChannel(tt.user, tt.channelAuth, "user"))
		})
	}
}

// channelWorkspaceUpdateAuthorized carries the delegation-scope policy that used
// to live inside the channel manager: a delegation caller may push a
// workspace-access update only to channels opened by the SAME bearer in the SAME
// delegated workspace, while an unscoped caller reaches any same-user channel
// not pinned to a different delegation workspace.
func TestChannelWorkspaceUpdateAuthorized(t *testing.T) {
	chInfo := func(cred auth.CredentialIdentity) channelmgr.ChannelInfo {
		return channelmgr.ChannelInfo{AuthInfo: channelmgr.AuthInfo{Credential: cred}}
	}
	tests := []struct {
		name    string
		caller  auth.CredentialIdentity
		channel auth.CredentialIdentity
		want    bool
	}{
		{"delegation caller same bearer + workspace", auth.DelegationCredential("d1", "ws-1", "worker-mint"), auth.DelegationCredential("d1", "ws-1", "worker-mint"), true},
		{"delegation caller other bearer", auth.DelegationCredential("d1", "ws-1", "worker-mint"), auth.DelegationCredential("d2", "ws-1", "worker-mint"), false},
		{"delegation caller other workspace", auth.DelegationCredential("d1", "ws-1", "worker-mint"), auth.DelegationCredential("d1", "ws-2", "worker-mint"), false},
		{"delegation caller cannot reach cookie channel", auth.DelegationCredential("d1", "ws-1", "worker-mint"), auth.SessionCredential("s1"), false},
		{"delegation caller cannot reach api channel", auth.DelegationCredential("d1", "ws-1", "worker-mint"), auth.APICredential("api-1"), false},
		{"unscoped caller reaches cookie channel", auth.SessionCredential("s1"), auth.SessionCredential("s2"), true},
		{"unscoped caller reaches same-workspace delegation channel", auth.SessionCredential("s1"), auth.DelegationCredential("d1", "ws-1", "worker-mint"), true},
		{"unscoped caller skips other-workspace delegation channel", auth.SessionCredential("s1"), auth.DelegationCredential("d1", "ws-2", "worker-mint"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorize := channelWorkspaceUpdateAuthorized(tt.caller, "ws-1")
			assert.Equal(t, tt.want, authorize(chInfo(tt.channel)))
		})
	}
}

// verifyDelegationWorkerScope's two store-free arms.
//
// The cross-tenant case (target != minter, minter owned by someone else) needs a
// seeded worker row and is covered end-to-end in
// channel_service_delegation_test.go. What is only reachable here is the pair of
// decisions the method makes BEFORE touching the store -- and they fail in
// opposite directions, so both matter:
//
//   - a non-delegation caller must pass untouched, or the gate would lock every
//     cookie and API user out of their own workers;
//   - a token always reaches the worker that MINTED it, without a store lookup.
//
// The "no recorded minter" arm lives at the constructor instead: DelegationCredential
// rejects an empty minter outright, so package service cannot build that state to
// test it (see the note inline below).
func TestVerifyDelegationWorkerScopeStoreFreeArms(t *testing.T) {
	// No store: neither arm may reach one. A nil store makes that mechanical --
	// if either arm starts doing a lookup, this test panics instead of passing.
	s := &ChannelService{}
	ctx := context.Background()

	for _, cred := range []auth.CredentialIdentity{
		auth.SessionCredential("session-1"),
		auth.APICredential("api-1"),
	} {
		user := &auth.UserInfo{ID: "user-1", Credential: cred}
		assert.NoError(t, s.verifyDelegationWorkerScope(ctx, user, "worker-target"),
			"a non-delegation credential must not be gated on the minting worker")
	}

	// The "no recorded minter" arm is deliberately NOT exercised here:
	// auth.DelegationCredential now panics on an empty minter, so that state cannot
	// be constructed from outside package auth at all. The invariant it used to pin
	// moved one layer earlier and is asserted at the constructor, by
	// auth.TestCredentialIdentityRejectsIncompleteStoredCredentials.
	// verifyDelegationWorkerScope keeps its own empty-minter refusal as defence in
	// depth against an in-package struct literal that bypasses the constructor.

	// The minting worker itself stays reachable without a store lookup -- the
	// common `leapmux remote` path must not pay for a query, nor fail when the
	// target and minter already match.
	self := &auth.UserInfo{
		ID:         "user-1",
		Credential: auth.DelegationCredential("d1", "ws-1", "worker-mint"),
	}
	assert.NoError(t, s.verifyDelegationWorkerScope(ctx, self, "worker-mint"),
		"a token must always reach the worker that minted it")
}

// channelClosedByWorkspaceRemoval closes unscoped channels (they carry the
// user's full accessible-workspace snapshot) and delegation channels pinned to
// the removed workspace, sparing delegation channels for other workspaces.
func TestChannelClosedByWorkspaceRemoval(t *testing.T) {
	chInfo := func(cred auth.CredentialIdentity) channelmgr.ChannelInfo {
		return channelmgr.ChannelInfo{AuthInfo: channelmgr.AuthInfo{Credential: cred}}
	}
	authorize := channelClosedByWorkspaceRemoval("ws-1")
	assert.True(t, authorize(chInfo(auth.SessionCredential("s1"))), "unscoped cookie channel must close")
	assert.True(t, authorize(chInfo(auth.APICredential("api-1"))), "unscoped api channel must close")
	assert.True(t, authorize(chInfo(auth.DelegationCredential("d1", "ws-1", "worker-mint"))), "delegation channel for the removed workspace must close")
	assert.False(t, authorize(chInfo(auth.DelegationCredential("d1", "ws-2", "worker-mint"))), "delegation channel for another workspace must survive")
}
