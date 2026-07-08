package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// createClaimTestAgent creates a minimal agent row so the durable answer claim (a control_response_answers
// row FK-referencing agents) can be inserted.
func createClaimTestAgent(t *testing.T, svc *Context, id string) {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: id, WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
}

// TestClaimControlResponseAnswer pins the per-INSTANCE idempotency claim SendControlResponse uses to
// dedup a duplicate answer (an RPC retry or a second window echoing the SAME claim_token): the first
// claim for a (agent, request, token) triple wins and a repeat loses, claims are isolated per request,
// per agent, and per token, a DIFFERENT token for the same request id claims fresh (the reused-instance
// case), and CleanupAgent (which also runs on a transient restart) does NOT clear the durable claim.
func TestClaimControlResponseAnswer(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	createClaimTestAgent(t, svc, "agent-1")
	createClaimTestAgent(t, svc, "agent-2")

	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "req-1", "tokA"), "the first claim wins")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "req-1", "tokA"),
		"a repeat claim with the SAME token loses -- the duplicate answer draws no second row")

	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "req-2", "tokA"),
		"a different request on the same agent is independent")
	assert.True(t, svc.Output.claimControlResponseAnswer("agent-2", "req-1", "tokA"),
		"the same request id on a different agent is independent")

	// A REUSED request id whose NEW instance carries a DIFFERENT token claims fresh, while the prior
	// instance's token stays claimed -- so a stale duplicate of the prior instance (tokA) still loses.
	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "req-1", "tokB"),
		"the reissued instance's fresh token claims a distinct key")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "req-1", "tokA"),
		"a stale duplicate of the PRIOR instance (old token) still loses")

	// An empty token (a pre-token answer, or a frontend lookup miss) degrades to request_id-only dedup.
	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "req-3", ""), "empty-token first claim wins")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "req-3", ""),
		"a repeat empty-token claim for the same request loses (id-only dedup fallback)")

	// CleanupAgent runs on a transient context reset and does NOT touch the durable claim rows.
	svc.Output.CleanupAgent("agent-1")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "req-2", "tokA"),
		"the durable claim survives CleanupAgent (a transient restart), so the duplicate still loses")
}

// TestClaimControlResponseAnswer_ConcurrentClaimsExactlyOneWins pins the atomicity the dedup rests on:
// N simultaneous claims for the SAME (agent, request, token) -- the concurrent two-window case the
// claim exists to serialize -- must yield exactly ONE winner, never two persisting rows. The
// (agent_id, request_id, claim_token) primary key is the serialization point. Run with -race to also
// exercise the shared *sql.DB.
func TestClaimControlResponseAnswer_ConcurrentClaimsExactlyOneWins(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	createClaimTestAgent(t, svc, "agent-1")

	const n = 64
	var wins int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximize contention
			if svc.Output.claimControlResponseAnswer("agent-1", "req-1", "tokA") {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), wins, "exactly one concurrent claim wins; the rest are deduped duplicates")
}

// TestClaimControlResponseAnswer_FailsOpenOnError pins the fail-OPEN guard on the durable claim: if
// the INSERT errors -- here a foreign-key violation because no agent row exists (in production
// requireAccessibleAgent guarantees one does) -- the claim returns true (treat as first) rather than
// false, so a transient DB error never silently drops the user's answer. A rare duplicate row is the
// deliberate lesser evil.
func TestClaimControlResponseAnswer_FailsOpenOnError(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	// No agent created -> the control_response_answers -> agents(id) foreign key rejects the INSERT.
	assert.True(t, svc.Output.claimControlResponseAnswer("ghost-agent", "req-1", "tokA"),
		"a claim whose INSERT errors fails open (true) so the answer is never dropped")
}

// TestClaimControlResponseAnswer_ReusedRequestIDDistinctTokenClaimsFresh is the id-reuse closure guard.
// A subprocess relaunch (the per-exit ClearPendingControlRequests path) does NOT clear the durable
// claims -- so a DUPLICATE of a pre-relaunch answer straddling the restart (carrying the PRIOR
// instance's token) is still deduped, closing the double-persist window. Yet the genuine post-relaunch
// answer -- to a re-issued request whose new instance minted a FRESH token -- claims a distinct key and
// is NOT rejected as a duplicate. No release-on-reissue is involved; the two are told apart purely by
// their echoed claim_token. Contrast the OLD release-based design, where a reused id reopened the dedup
// window for the prior instance's lagging duplicate.
func TestClaimControlResponseAnswer_ReusedRequestIDDistinctTokenClaimsFresh(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	createClaimTestAgent(t, svc, "agent-1")

	// The pre-relaunch subprocess answered Codex/ACP-style request id "2" (instance A, token "instA").
	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "2", "instA"), "instance A's answer wins")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "2", "instA"),
		"a same-instance duplicate (same token) still loses")

	// The subprocess exits/relaunches: ClearPendingControlRequests deletes DB requests but does NOT
	// drop the durable claims.
	svc.Output.ClearPendingControlRequests("agent-1")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "2", "instA"),
		"a duplicate of instance A straddling the relaunch (old token) is still deduped")

	// The NEW subprocess re-issues request id "2" -- a fresh INSTANCE minting a distinct token. Its
	// genuine answer (token "instB") claims a distinct key and persists, without any release step.
	assert.True(t, svc.Output.claimControlResponseAnswer("agent-1", "2", "instB"),
		"the reissued instance's fresh token claims fresh -- the genuine post-relaunch answer is not withheld")
	assert.False(t, svc.Output.claimControlResponseAnswer("agent-1", "2", "instA"),
		"instance A's stale duplicate STILL loses even after instance B claimed -- the reuse window stays closed")
}

// TestPersistControlRequest_MintsFreshClaimTokenPerInstance pins that PersistControlRequest stamps a
// distinct claim_token on each store of a (reused) request id, so the token the frontend echoes back
// distinguishes instances. This is the store-side half of the id-reuse closure. It ALSO pins the
// thread-through guarantee the live broadcast relies on: the token PersistControlRequest RETURNS is
// exactly the one it stored, so the paired BroadcastControlRequest can carry it without a second
// GetControlRequest readback (and without the readback-failure window that broadcast an empty token).
func TestPersistControlRequest_MintsFreshClaimTokenPerInstance(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	createClaimTestAgent(t, svc, "agent-1")
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	returned1 := sink.PersistControlRequest("2", []byte(`{"jsonrpc":"2.0","id":2,"method":"session/request_permission","params":{}}`))
	first, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: "agent-1", RequestID: "2"})
	require.NoError(t, err)
	assert.NotEmpty(t, first.ClaimToken, "a stored control request carries a claim token the frontend echoes back")
	assert.Equal(t, first.ClaimToken, returned1,
		"PersistControlRequest returns the SAME token it stored, so the paired broadcast carries it without a readback")

	// Re-store the same id (a reissued instance). The token must be a DIFFERENT one so the two
	// instances' answers claim distinct keys.
	returned2 := sink.PersistControlRequest("2", []byte(`{"jsonrpc":"2.0","id":2,"method":"session/request_permission","params":{}}`))
	second, err := svc.Queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: "agent-1", RequestID: "2"})
	require.NoError(t, err)
	assert.NotEmpty(t, second.ClaimToken)
	assert.Equal(t, second.ClaimToken, returned2, "the re-store returns the fresh token it stored")
	assert.NotEqual(t, first.ClaimToken, second.ClaimToken,
		"re-issuing a request id mints a fresh token so a stale duplicate of the prior instance can't re-win")
}
