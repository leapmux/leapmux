package service

import (
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
)

func userCanUseChannel(user *auth.UserInfo, channelAuth channelmgr.AuthInfo, channelUserID string) bool {
	if user == nil || !user.ID.Matches(channelUserID) {
		return false
	}
	return channelAuth.Credential.Matches(user.Credential)
}

func channelAuthInfo(user *auth.UserInfo) channelmgr.AuthInfo {
	if user == nil {
		return channelmgr.AuthInfo{}
	}
	return channelmgr.AuthInfo{
		Credential:          user.Credential,
		UserAuthGeneration:  user.UserAuthGeneration,
		CredentialExpiresAt: user.CredentialExpiresAt,
	}
}

// channelWorkspaceUpdateAuthorized authorizes pushing a workspace-access update
// for workspaceID to a channel. Unscoped callers reach every same-user channel
// not pinned to a different delegation workspace; a delegation caller reaches
// only channels opened by the same bearer in the same delegated workspace, never
// an unrestricted cookie/API channel. Delegation-scope policy lives here beside
// userCanUseChannel, not inside the channel manager's routing index.
func channelWorkspaceUpdateAuthorized(caller auth.CredentialIdentity, workspaceID string) func(channelmgr.ChannelInfo) bool {
	return func(info channelmgr.ChannelInfo) bool {
		channelScope := info.AuthInfo.Credential.WorkspaceScopeID()
		if caller.IsDelegation() {
			return info.AuthInfo.Credential.Matches(caller) && channelScope == workspaceID
		}
		return channelScope == "" || channelScope == workspaceID
	}
}

// channelClosedByWorkspaceRemoval authorizes closing a channel when
// workspaceID's access is removed: unscoped channels carry the user's full
// accessible-workspace snapshot and must close, while a delegation channel is
// immutable to one workspace, so unrelated scopes survive.
func channelClosedByWorkspaceRemoval(workspaceID string) func(channelmgr.ChannelInfo) bool {
	return func(info channelmgr.ChannelInfo) bool {
		scope := info.AuthInfo.Credential.WorkspaceScopeID()
		return scope == "" || scope == workspaceID
	}
}
