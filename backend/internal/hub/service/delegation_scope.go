package service

import (
	"errors"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// submitWorkerBound is the ONE function that answers "may this caller bind a
// tab to this worker" for SubmitOps, and it composes BOTH bounds.
//
// SubmitOps is one procedure that mutates the whole layout document, so
// workspace:write is the honest method scope -- every client-submittable op
// body is layout. What a scope can still narrow is SetTabRegisterOp.worker_id,
// which points a tab at a MACHINE, and two separate rules limit it:
//
//   - the delegation bound, which stops a bearer minted by worker A from
//     binding a tab to worker B, and
//   - worker:read, without which an app was never granted the right to reach
//     a machine at all. An app given workspace:write alone could otherwise
//     bind a tab to any worker its owner has -- the layout write is the reach.
//
// They compose in one place because taking one without the other is the whole
// failure mode. Two call sites would each look complete, and the seam already
// exists as a single predicate, so a caller physically cannot pass half of it.
//
// A nil result means UNBOUNDED, which is what the crdt package reads for "no
// narrowing" -- so the unscoped, non-delegation caller still returns nil and
// the decorator is not installed at all.
func submitWorkerBound(user *auth.UserInfo, scope auth.DelegationWorkerScope) func(string) bool {
	// A caller the transport could not attribute reaches no machine. This is
	// the arm where nil would be read as UNBOUNDED, so it is stated before the
	// scope question rather than folded into it.
	if user == nil {
		return denyEveryWorker
	}
	if !user.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKER_READ) {
		// The scope refuses EVERY worker, so the composition is the constant
		// false rather than an intersection with the delegation bound.
		return denyEveryWorker
	}
	return workerScopePredicate(scope)
}

// denyEveryWorker is the constant refusal submitWorkerBound returns. It is a
// named function rather than a literal at each site so the two refusals are
// visibly the same answer.
func denyEveryWorker(string) bool { return false }

// rejectDelegationBearer refuses an operation outright for a delegation bearer.
// Used for workspace lifecycle mutations (create / rename / delete), which a
// prompt-injectable agent has no business performing on its user's behalf.
func rejectDelegationBearer(user *auth.UserInfo, operation string) error {
	if user == nil || !user.Credential.IsDelegation() {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, errors.New(operation+" is not allowed for delegation tokens"))
}
