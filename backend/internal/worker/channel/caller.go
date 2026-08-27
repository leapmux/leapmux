package channel

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// Caller is WHO made an inner RPC and WHAT they were granted, together.
//
// It replaces the bare userid.UserID that every handler used to take, and the
// pairing is the whole point. The Worker restricted each method on identity
// alone -- "is this the registrant", one bit -- so a credential that could open a
// channel reached ReadFile, SendInput, PushBranch and OpenTunnelConn alike.
// Threading a value that carries BOTH facts is what makes forgetting the second
// one impossible rather than merely unlikely: the compiler refuses every
// unconverted call site.
//
// The ZERO Caller grants nothing. Its Scopes are the empty set, which allows no
// scope at all, so a path that constructs one by accident denies rather than
// admits. The one site that legitimately has no limit says so explicitly; see
// LocalAgentCaller.
//
// It is a VALUE, comparable and copyable, so a handler can hold one without a
// lifetime question.
type Caller struct {
	// UserID is the identity the HUB established. A worker never derives it
	// from anything the E2EE client sent; see ChannelOpenRequest.user_id.
	UserID userid.UserID
	// Scopes is what the credential that opened this channel was granted, as
	// the Hub announced it at the handshake.
	Scopes authscope.ScopeSet
}

// NewCaller pairs an identity with a grant.
func NewCaller(userID userid.UserID, scopes authscope.ScopeSet) Caller {
	return Caller{UserID: userID, Scopes: scopes}
}

// LocalAgentCaller is the caller for the Worker's own local IPC socket.
//
// It states an UNSCOPED grant explicitly, and it is the only site that has one.
// The socket is reachable by a process on the machine running as the worker's
// own user, which is already the authority every scope subdivides -- there is
// nothing for a scope to subtract there, and the credential model does not
// reach that far.
//
// Spelling it out rather than leaving the zero value is what keeps every OTHER
// construction fail-closed: a path that forgets to fill Scopes reaches nothing,
// because "no limit" is a value somebody wrote rather than a default.
func LocalAgentCaller(userID userid.UserID) Caller {
	return Caller{UserID: userID, Scopes: authscope.UnscopedGrant()}
}

// Allows reports whether this caller's grant reaches one scope.
func (c Caller) Allows(scope leapmuxv1.Scope) bool { return c.Scopes.Allows(scope) }
