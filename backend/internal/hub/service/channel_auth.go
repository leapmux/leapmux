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
