package crdt_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

// TestRegistry_Get_RefusesBlankUserID pins the registry's tenancy floor: the
// map key IS the tenant, and the factory builds the Manager from that key, so a
// blank key would mint a phantom "" manager that owns a CRDT belonging to
// nobody -- and every submit routed to it would commit there, since a submit
// names no tenant of its own to be checked against. Get must refuse before the
// factory runs.
func TestRegistry_Get_RefusesBlankUserID(t *testing.T) {
	var factoryCalls atomic.Int32
	journal := newFakeJournal()
	factory := func(_ context.Context, userID userid.UserID) (*crdt.Manager, error) {
		factoryCalls.Add(1)
		mgr := crdt.NewManager(userID, journal, allowAll{}, nil, time.Now)
		require.NoError(t, mgr.Bootstrap(context.Background()))
		return mgr, nil
	}

	registry := crdt.NewRegistry(factory, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	mgr, err := registry.Get(context.Background(), "")
	require.ErrorIs(t, err, crdt.ErrBlankUserID)
	assert.Nil(t, mgr, "no manager may be handed out for a blank tenancy key")
	assert.Zero(t, factoryCalls.Load(), "the factory must not bootstrap a phantom tenant")
}

// TestSubmitRefusesABlankTenantManager pins the manager-side half of the same
// invariant, for a manager built directly with NewManager rather than through
// Registry.Get (tests, and any future in-process caller).
//
// A submit no longer names the tenant it is addressed to -- SubmitInput carries
// no user id, so "this submit landed on the wrong tenant's manager" cannot be
// spelled and there is nothing to compare. What survives is the manager with NO
// tenant: it would commit ops into a CRDT belonging to nobody and key a
// user_state row by "". service.crdtJournal's errBlankTenant refuses that at the
// REAL journal only, so a manager wired to a fake or in-memory journal -- which
// is exactly what this test does -- needs the manager-side refusal to stop it.
func TestSubmitRefusesABlankTenantManager(t *testing.T) {
	batch := func() []*leapmuxv1.OpBatch {
		return []*leapmuxv1.OpBatch{{
			BatchId: id.Generate(),
			Ops: []*leapmuxv1.CrdtOp{{
				OpId: id.Generate(),
				Body: &leapmuxv1.CrdtOp_TombstoneTab{
					TombstoneTab: &leapmuxv1.TombstoneTabOp{
						TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
						TabId:   "tab-1",
					},
				},
			}},
		}}
	}

	t.Run("blank manager refuses to commit", func(t *testing.T) {
		mgr, _, _ := runManager(t, "", allowAll{}, 1000)
		_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
			Batches: batch(),
		})
		require.Error(t, err, "a manager with no tenant must not commit anything")
		// Pin WHICH refusal fired: without this the subtest would also pass if
		// SubmitInternal started failing for an unrelated reason.
		assert.ErrorContains(t, err, "blank user id")
	})

	t.Run("tenant manager commits", func(t *testing.T) {
		// Control: the refusal above is about the blank tenancy key, not about
		// SubmitInternal having become unconditionally broken. Without it this
		// test would still pass if every submit errored.
		mgr, _, _ := runManager(t, "user-1", allowAll{}, 1000)
		_, err := mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
			Batches: batch(),
		})
		require.NoError(t, err)
	})
}

// TestBootstrapRefusesAStatePayloadNamingAnotherTenant pins the third face of
// the same invariant: after Registry.Get fixes the tenancy KEY and every submit
// inherits it from the manager it lands on, Bootstrap must not let the loaded
// PAYLOAD re-open the question.
//
// The blast radius is why it matters. CompactBatch keys the next user_state row
// by state.GetUserId(), so a manager that adopted a foreign payload would
// rewrite another tenant's state -- and one that adopted a blank payload would
// key a row by "", which the store now refuses outright. The derived tab-index
// rows were the sharper half of that radius until service.txTabIndexWriter
// began stamping the committing tenant and workspace_tab_owned.user_id gained
// its users(id) FK; before those, a blank payload produced blank-owner rows the
// store could not delete, so the worker reconciler kept their agents and
// terminals alive with no API able to clear them.
//
// Named by internal/audit.identityComparisonSites for
// internal/hub/crdt.(*Manager).requireOwnState.
func TestBootstrapRefusesAStatePayloadNamingAnotherTenant(t *testing.T) {
	bootstrapWithPayload := func(t *testing.T, mgrUser, payloadUser string) error {
		t.Helper()
		j := newFakeJournal()
		state := crdt.NewState(payloadUser)
		j.seedState(state)
		// A blank mgrUser is one of the cases under test ("blank tenant on both
		// sides"), and userid.UserID's zero value is constructible -- which is
		// exactly why requireOwnState keeps its own blank refusal. MustNew would
		// panic before the manager could be built.
		owner := userid.UserID{}
		if mgrUser != "" {
			owner = userid.MustNew(mgrUser)
		}
		return crdt.NewManager(owner, j, allowAll{}, nil, time.Now).Bootstrap(context.Background())
	}

	t.Run("foreign tenant", func(t *testing.T) {
		err := bootstrapWithPayload(t, "user-1", "user-2")
		require.Error(t, err, "a payload naming another tenant must stop the manager, not be adopted")
		assert.Contains(t, err.Error(), "user-2")
		assert.Contains(t, err.Error(), "user-1")
	})

	t.Run("blank tenant in the payload", func(t *testing.T) {
		// The case a bare `!=` would still catch, but only for a non-blank
		// manager; it is here because it is the one that used to produce the
		// undeletable blank-owner tab rows, and it is still the one that would
		// key a user_state row by "".
		require.Error(t, bootstrapWithPayload(t, "user-1", ""))
	})

	t.Run("blank tenant on both sides", func(t *testing.T) {
		// The fail-OPEN a bare `!=` would wave through: two blank ids are equal.
		require.Error(t, bootstrapWithPayload(t, "", ""),
			"empty-vs-empty must not satisfy the tenancy check")
	})

	t.Run("matching tenant bootstraps", func(t *testing.T) {
		// Control: the refusals above are about the id, not about Bootstrap
		// having become unconditionally broken.
		require.NoError(t, bootstrapWithPayload(t, "user-1", "user-1"))
	})
}
