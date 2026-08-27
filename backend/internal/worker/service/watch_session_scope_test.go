package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// WatchEvents carries agents and terminals on ONE stream, so its method scope
// can only be a floor: a credential granted agent:read and not terminal:read
// must reach the stream and see nothing of the second kind.
//
// The refusal is per ENTITY, through the machinery the stream already has --
// WatchUpdateAck.rejected_agents and rejected_terminals -- so a client learns
// which of the things it asked for it will not receive. Dropping them silently
// would leave it waiting for output that never comes, on a subscription it
// believes is live.
func TestPartitionByScope(t *testing.T) {
	t.Parallel()

	caller := func(scopes ...leapmuxv1.Scope) channel.Caller {
		return channel.NewCaller(userid.MustNew("u1"), authscope.MustNew(scopes...))
	}
	agents := []*leapmuxv1.WatchAgentEntry{
		{AgentId: "agent-1"}, {AgentId: "agent-2"},
	}

	t.Run("a caller with the scope keeps its whole request", func(t *testing.T) {
		t.Parallel()
		req, denied := partitionByScope(
			caller(leapmuxv1.Scope_SCOPE_AGENT_READ), agents,
			leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)
		assert.Equal(t, agents, req)
		assert.Empty(t, denied)
	})

	t.Run("a caller without it gets one rejection per entity", func(t *testing.T) {
		t.Parallel()
		req, denied := partitionByScope(
			caller(leapmuxv1.Scope_SCOPE_TERMINAL_READ), agents,
			leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)

		// EMPTY, not the original: registration is replace-semantics, so a
		// grant that narrows must leave the session watching nothing of that
		// kind rather than keeping whatever it had before.
		assert.Empty(t, req, "a refused kind must leave the session watching none of it")

		require.Len(t, denied, len(agents), "the client must learn about every entity it asked for")
		for i, r := range denied {
			assert.Equal(t, agents[i].GetAgentId(), r.GetEntityId())
			assert.Equal(t, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN, r.GetReason())
		}
	})

	// An UNSCOPED caller is a session, and reaches both kinds. Without this the
	// case above would pass for a gate that refused everybody.
	t.Run("an unscoped caller reaches every kind", func(t *testing.T) {
		t.Parallel()
		unscoped := channel.NewCaller(userid.MustNew("u1"), authscope.UnscopedGrant())
		req, denied := partitionByScope(unscoped, agents, leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)
		assert.Equal(t, agents, req)
		assert.Empty(t, denied)
	})

	// The ZERO caller reaches nothing, which is the fail-closed answer for a
	// request the transport could not attribute.
	t.Run("a zero caller reaches nothing", func(t *testing.T) {
		t.Parallel()
		req, denied := partitionByScope(channel.Caller{}, agents,
			leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)
		assert.Empty(t, req)
		assert.Len(t, denied, len(agents))
	})

	// The TWO kinds are gated separately on the same stream, which is the whole
	// reason the rule is per entity rather than per method.
	t.Run("the two kinds are independent", func(t *testing.T) {
		t.Parallel()
		terminals := []*leapmuxv1.WatchTerminalEntry{{TerminalId: "term-1"}}
		agentOnly := caller(leapmuxv1.Scope_SCOPE_AGENT_READ)

		agentReq, agentDenied := partitionByScope(agentOnly, agents,
			leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)
		termReq, termDenied := partitionByScope(agentOnly, terminals,
			leapmuxv1.Scope_SCOPE_TERMINAL_READ, terminalEntryIDs)

		assert.Len(t, agentReq, len(agents), "the granted kind is served")
		assert.Empty(t, agentDenied)
		assert.Empty(t, termReq, "the ungranted kind is not")
		assert.Len(t, termDenied, len(terminals))
	})

	// An EMPTY request from a refused caller produces no rejections, because
	// there is nothing to report. A rejection list built from a nil slice must
	// not be a nil that reads as "something went wrong".
	t.Run("an empty request produces no rejections", func(t *testing.T) {
		t.Parallel()
		req, denied := partitionByScope(channel.Caller{}, []*leapmuxv1.WatchAgentEntry(nil),
			leapmuxv1.Scope_SCOPE_AGENT_READ, agentEntryIDs)
		assert.Empty(t, req)
		assert.Empty(t, denied)
	})
}
