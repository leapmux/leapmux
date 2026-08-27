package service

import (
	"fmt"

	"connectrpc.com/connect"
)

// rejectSolo refuses a surface that solo mode does not serve.
//
// Solo mode authenticates every request as one synthetic user, with no
// session row and no credential store, so a whole family of procedures has
// nothing to act on. Nine handlers wrote this refusal by hand, each with its
// own subject, and two more wrapped it once apiece -- one of the nine
// already lost its subject and read only "not available in solo mode". One
// helper keeps the code and the wording identical and leaves the subject as
// the only per-handler part.
//
// subject identifies what is unavailable, in the sentence "solo mode does not
// provide <subject>": "password changes", "CLI credentials", "sign-up".
// The sentence states the actor, which is also the one form that reads
// correctly for a plural subject and a singular one alike -- the nine
// hand-written copies chose their own verb, and a template that kept the passive
// would have to choose one of them for every caller.
//
// RequestPasswordReset is deliberately NOT a member. It answers solo mode
// with an empty SUCCESS rather than a refusal, because the response is
// uniform by design and a refusal there would be an enumeration oracle.
func rejectSolo(solo bool, subject string) error {
	if !solo {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("solo mode does not provide %s", subject))
}
