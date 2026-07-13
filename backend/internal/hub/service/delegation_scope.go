package service

import (
	"errors"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

// workerScopePredicate adapts a resolved worker scope to the predicate
// crdt.SubmitInput takes, collapsing an UNBOUNDED scope to nil.
//
// The nil is load-bearing, not a micro-optimisation: a non-nil predicate makes the
// validator wrap its AuthChecker in the scoped decorator, and handing that decorator
// an always-true predicate would add a wrapper that constrains nothing. nil says
// "this credential carries no worker bound" in the one way the crdt package reads.
func workerScopePredicate(scope auth.DelegationWorkerScope) func(string) bool {
	if !scope.IsBounded() {
		return nil
	}
	return scope.Allows
}

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

// scopedWorkspaceRequest is delegationScopedWorkspaceRequest's answer. The two
// fields exist because an empty Workspaces slice is AMBIGUOUS on its own:
// elsewhere an empty workspace list means "resolve to every workspace the user
// can read", but a delegation request that named only blank ids must be DENIED
// rather than widened. Encoding that as a named Deny field (rather than a
// positional bool beside the slice) makes the inversion impossible to consume
// by accident: a caller that ignores Deny reads an empty slice, and an empty
// slice resolves to nothing only through the deny branch it skipped -- so the
// field is the one place the distinction lives.
type scopedWorkspaceRequest struct {
	// Workspaces is the narrowed request. Meaningless when Deny is set.
	Workspaces []string
	// Deny reports that the request named only blank ids: the caller must
	// return an empty result, not widen to everything readable.
	Deny bool
}

// delegationScopedWorkspaceRequest narrows a workspace-id request to what a
// delegation bearer may see. A non-delegation caller passes through unchanged.
// See scopedWorkspaceRequest for why the deny case is a named field.
func delegationScopedWorkspaceRequest(user *auth.UserInfo, requested []string) (scopedWorkspaceRequest, error) {
	if user == nil || !user.Credential.IsDelegation() {
		return scopedWorkspaceRequest{Workspaces: requested}, nil
	}
	if len(requested) == 0 {
		return scopedWorkspaceRequest{Workspaces: []string{user.Credential.WorkspaceScopeID()}}, nil
	}
	matched := false
	for _, workspaceID := range requested {
		if workspaceID == "" {
			continue
		}
		if delegationWorkspaceMismatch(user, workspaceID) {
			return scopedWorkspaceRequest{}, connect.NewError(connect.CodePermissionDenied, errors.New("workspace outside delegation scope"))
		}
		matched = true
	}
	if !matched {
		return scopedWorkspaceRequest{Deny: true}, nil
	}
	return scopedWorkspaceRequest{Workspaces: []string{user.Credential.WorkspaceScopeID()}}, nil
}
