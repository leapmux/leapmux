package service

import (
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
		{"delegation caller same bearer + workspace", auth.DelegationCredential("d1", "ws-1"), auth.DelegationCredential("d1", "ws-1"), true},
		{"delegation caller other bearer", auth.DelegationCredential("d1", "ws-1"), auth.DelegationCredential("d2", "ws-1"), false},
		{"delegation caller other workspace", auth.DelegationCredential("d1", "ws-1"), auth.DelegationCredential("d1", "ws-2"), false},
		{"delegation caller cannot reach cookie channel", auth.DelegationCredential("d1", "ws-1"), auth.SessionCredential("s1"), false},
		{"delegation caller cannot reach api channel", auth.DelegationCredential("d1", "ws-1"), auth.APICredential("api-1"), false},
		{"unscoped caller reaches cookie channel", auth.SessionCredential("s1"), auth.SessionCredential("s2"), true},
		{"unscoped caller reaches same-workspace delegation channel", auth.SessionCredential("s1"), auth.DelegationCredential("d1", "ws-1"), true},
		{"unscoped caller skips other-workspace delegation channel", auth.SessionCredential("s1"), auth.DelegationCredential("d1", "ws-2"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorize := channelWorkspaceUpdateAuthorized(tt.caller, "ws-1")
			assert.Equal(t, tt.want, authorize(chInfo(tt.channel)))
		})
	}
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
	assert.True(t, authorize(chInfo(auth.DelegationCredential("d1", "ws-1"))), "delegation channel for the removed workspace must close")
	assert.False(t, authorize(chInfo(auth.DelegationCredential("d1", "ws-2"))), "delegation channel for another workspace must survive")
}
