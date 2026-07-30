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
//
// It is now the ONLY narrowing a delegation bearer carries. The workspace axis
// that used to sit beside it is gone: a Worker serves one user and stores no
// workspace id, so pinning a bearer to one workspace narrowed nothing a
// prompt-injectable agent could not already reach through its own tab. What it
// must not reach is another MACHINE of the same user -- which is exactly what
// this predicate answers.
func workerScopePredicate(scope auth.DelegationWorkerScope) func(string) bool {
	if !scope.IsBounded() {
		return nil
	}
	return scope.Allows
}

// rejectDelegationBearer refuses an operation outright for a delegation bearer.
// Used for workspace lifecycle mutations (create / rename / delete), which a
// prompt-injectable agent has no business performing on its user's behalf.
func rejectDelegationBearer(user *auth.UserInfo, operation string) error {
	if user == nil || !user.Credential.IsDelegation() {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, errors.New(operation+" is not allowed for delegation tokens"))
}
