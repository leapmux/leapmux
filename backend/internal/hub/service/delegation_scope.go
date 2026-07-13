package service

import (
	"errors"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func delegationWorkspaceMismatch(user *auth.UserInfo, workspaceID string) bool {
	return user != nil && user.Credential.IsDelegation() && workspaceID != user.Credential.WorkspaceScopeID()
}

func requireDelegationWorkspace(user *auth.UserInfo, workspaceID string) error {
	if !delegationWorkspaceMismatch(user, workspaceID) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, errors.New("workspace outside delegation scope"))
}

func rejectDelegationBearer(user *auth.UserInfo, operation string) error {
	if user == nil || !user.Credential.IsDelegation() {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, errors.New(operation+" is not allowed for delegation tokens"))
}

func requireDelegationWorkspaceOrNotFound(user *auth.UserInfo, workspaceID, message string) error {
	if !delegationWorkspaceMismatch(user, workspaceID) {
		return nil
	}
	return connect.NewError(connect.CodeNotFound, errors.New(message))
}

func delegationScopedWorkspaceRequest(user *auth.UserInfo, requested []string) ([]string, bool, error) {
	if user == nil || !user.Credential.IsDelegation() {
		return requested, false, nil
	}
	if len(requested) == 0 {
		return []string{user.Credential.WorkspaceScopeID()}, false, nil
	}
	matched := false
	for _, workspaceID := range requested {
		if workspaceID == "" {
			continue
		}
		if delegationWorkspaceMismatch(user, workspaceID) {
			return nil, false, connect.NewError(connect.CodePermissionDenied, errors.New("workspace outside delegation scope"))
		}
		matched = true
	}
	if !matched {
		return nil, true, nil
	}
	return []string{user.Credential.WorkspaceScopeID()}, false, nil
}
