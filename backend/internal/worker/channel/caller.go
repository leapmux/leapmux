package channel

import (
	"errors"
	"fmt"

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
	// SourceTabID is the tab whose process made the call: the agent or
	// terminal tab that the local IPC control token was minted for, or the tab
	// that a sibling Worker states for a call that it relays from one of its
	// own tabs (see RelayWorkerID). The Worker reads it from its own token
	// store or from that sibling, never from the client, so a caller cannot
	// forge it, and a handler can stamp it as provenance. It is empty for a
	// call that the browser or the CLI outside a tab makes over an E2EE
	// channel, and for a quake terminal, which has no tab.
	SourceTabID string
	// SourceAgent is the agent of SourceTabID when a sibling Worker relayed
	// the call and stated it, and the zero value otherwise. This Worker holds
	// no row for an agent of another Worker, so it reads the agent from here;
	// for a local call it reads its own row.
	SourceAgent SourceAgent
	// RelayWorkerID is the sibling Worker that opened this channel to relay
	// the calls of the processes in its tabs, as the Hub announced it at the
	// handshake (ChannelOpenRequest.relay_worker_id). It is empty for every
	// other caller. Only such a caller may state a CallSource (see
	// withSource).
	RelayWorkerID string
}

// SourceAgent is the agent that made a relayed call: its provider and its
// title when the call was made. The zero value states no agent.
type SourceAgent struct {
	Provider leapmuxv1.AgentProvider
	Title    string
}

// IsZero reports whether a states no agent.
func (a SourceAgent) IsZero() bool {
	return a.Provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED
}

// withSource returns the caller of a request that states source, the tab of
// a sibling Worker that made a relayed call. A request with no source keeps
// the caller as it is.
//
// Only a caller with a RelayWorkerID may state a source, because the Hub
// vouches for that relay alone. The browser, the CLI and a local process are
// refused: a source that one of them could state would claim a tab, and so
// an agent, of the caller's choosing.
func (c Caller) withSource(source *leapmuxv1.CallSource) (Caller, error) {
	if source == nil {
		return c, nil
	}
	if c.RelayWorkerID == "" {
		return Caller{}, errors.New("only a sibling Worker that relays a call may state its source")
	}
	if source.GetTabId() == "" {
		return Caller{}, errors.New("the call source states no tab")
	}
	c.SourceTabID = source.GetTabId()
	c.SourceAgent = SourceAgent{}
	if agent := source.GetAgent(); agent != nil {
		provider := agent.GetProvider()
		if _, known := leapmuxv1.AgentProvider_name[int32(provider)]; !known ||
			provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
			return Caller{}, fmt.Errorf("the agent of the call source states no known provider: %d", provider)
		}
		c.SourceAgent = SourceAgent{Provider: provider, Title: agent.GetTitle()}
	}
	return c, nil
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
