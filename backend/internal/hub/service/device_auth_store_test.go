package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func testDeviceHash(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func TestMemoryDeviceAuthorizationStore_LifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("digest length, uniqueness, and pending defaults", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()

		for _, bad := range [][]byte{nil, {}, make([]byte, 31), make([]byte, 33)} {
			assert.ErrorIs(t, s.Create(ctx, CreateDeviceAuthorizationParams{
				DeviceCodeHash: bad, UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
			}), store.ErrInvalidArgument)
		}

		h1 := testDeviceHash("code-1")
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: h1, UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID,
			DeviceName: "laptop", RequestedScopes: "workspace:read", ExpiresAt: now.Add(time.Hour),
		}))

		// Duplicate deviceCodeHash and duplicate userCode both return ErrConflict.
		assert.ErrorIs(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: h1, UserCode: "ZZZZZZ", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}), store.ErrConflict)
		assert.ErrorIs(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("code-2"), UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}), store.ErrConflict)

		row, err := s.Get(ctx, h1)
		require.NoError(t, err)
		assert.Equal(t, DeviceAuthorizationStatePending, row.State)
		assert.Empty(t, row.UserID)
		assert.Empty(t, row.GrantedScopes)

		// Defensive clone on Get.
		row.DeviceCodeHash[0] ^= 0xff
		again, err := s.Get(ctx, h1)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(h1, again.DeviceCodeHash))
	})

	t.Run("touch poll, approve, deny, consume, and retention until sweep", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hPending := testDeviceHash("pending")
		hDenied := testDeviceHash("denied")
		hApproved := testDeviceHash("approved")

		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hPending, UserCode: "PEND01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hDenied, UserCode: "DENY01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hApproved, UserCode: "APPR01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))

		// Zero UserID refused on ApproveByUserCode.
		_, err := s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "PEND01"}, now)
		assert.ErrorIs(t, err, store.ErrInvalidArgument)

		// TouchPoll updates LastPolledAt.
		require.NoError(t, s.TouchPoll(ctx, hPending, now))
		got, err := s.Get(ctx, hPending)
		require.NoError(t, err)
		require.NotNil(t, got.LastPolledAt)
		assert.True(t, got.LastPolledAt.Equal(now))

		// Deny is final.
		n, err := s.DenyByUserCode(ctx, "DENY01")
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		n, err = s.DenyByUserCode(ctx, "DENY01")
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "DENY01", UserID: alice}, now)
		require.NoError(t, err)
		assert.Zero(t, n)

		// Approve is single-use and rejects expired grants.
		n, err = s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: alice}, now.Add(2*time.Hour))
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: alice, GrantedScopes: "workspace:read", AuthGeneration: 3}, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		n, err = s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: bob, GrantedScopes: "admin:read"}, now)
		require.NoError(t, err)
		assert.Zero(t, n)

		// Only approved, live grant is consumable; consumed row stays readable until DeleteExpired.
		n, err = s.Consume(ctx, hPending, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.Consume(ctx, hDenied, now)
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.Consume(ctx, hApproved, now.Add(time.Hour)) // at exact expiry: refused
		require.NoError(t, err)
		assert.Zero(t, n)
		n, err = s.Consume(ctx, hApproved, now)
		require.NoError(t, err)
		assert.EqualValues(t, 1, n)
		n, err = s.Consume(ctx, hApproved, now)
		require.NoError(t, err)
		assert.Zero(t, n)

		got, err = s.Get(ctx, hApproved)
		require.NoError(t, err, "consumed row must stay readable until DeleteExpired sweeps it")
		require.NotNil(t, got.ConsumedAt)
		assert.Equal(t, int64(3), got.AuthGeneration)
	})

	t.Run("delete expired boundaries and capacity cap lazy eviction", func(t *testing.T) {
		cur := now
		s := NewMemoryDeviceAuthorizationStore()
		s.maxEntries = 3
		s.now = func() time.Time { return cur }

		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-exp"), UserCode: "EXP001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-at"), UserCode: "ATC001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur,
		}))
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-live"), UserCode: "LIV001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Millisecond),
		}))

		// Strict < boundary: only d-exp is swept; d-at and d-live remain.
		assert.EqualValues(t, 1, s.DeleteExpired(cur))
		_, err := s.Get(ctx, testDeviceHash("d-exp"))
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Get(ctx, testDeviceHash("d-at"))
		require.NoError(t, err)
		_, err = s.Get(ctx, testDeviceHash("d-live"))
		require.NoError(t, err)

		// Fill to capacity with live entries; 4th insert refused.
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-fill"), UserCode: "FIL001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}))
		assert.ErrorIs(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-over"), UserCode: "OVR001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}), errDeviceAuthCapacity)

		// Advance clock past d-at and d-live: lazy eviction allows new insert.
		cur = cur.Add(time.Second)
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-over"), UserCode: "OVR001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}))
	})
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseConsumeAndRollback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewMemoryDeviceAuthorizationStore()

	h := testDeviceHash("two-phase-consume")
	require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
		DeviceCodeHash: h, UserCode: "TPC001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
	}))
	_, err := s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "TPC001", UserID: alice}, now)
	require.NoError(t, err)

	res, ok := s.ReserveConsume(h, now)
	require.True(t, ok)
	assert.False(t, s.IsInvalidated(res))

	// Concurrent poller is blocked while reserved.
	_, ok2 := s.ReserveConsume(h, now)
	assert.False(t, ok2)

	// Release makes the grant retryable.
	s.ReleaseConsume(res)
	row, err := s.Get(ctx, h)
	require.NoError(t, err)
	assert.Nil(t, row.ConsumedAt)

	res2, ok := s.ReserveConsume(h, now)
	require.True(t, ok)
	s.CommitConsume(res2, now)

	row, err = s.Get(ctx, h)
	require.NoError(t, err)
	require.NotNil(t, row.ConsumedAt)
	_, ok3 := s.ReserveConsume(h, now)
	assert.False(t, ok3)
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseStepUpApproval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewMemoryDeviceAuthorizationStore()

	h := testDeviceHash("stepup")
	require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
		DeviceCodeHash: h, UserCode: "STP001", ClientID: oauthapp.ControlCLIClientID,
		ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-1",
	}))

	res, ok := s.ReserveApproveByUserCode("STP001", alice, now)
	require.True(t, ok)

	// Concurrent approve or deny is refused while reserved.
	_, ok2 := s.ReserveApproveByUserCode("STP001", alice, now)
	assert.False(t, ok2)
	n, err := s.DenyByUserCode(ctx, "STP001")
	require.NoError(t, err)
	assert.Zero(t, n)

	// Release reverts cleanly to pending with empty UserID.
	s.ReleaseApproveByUserCode(res)
	row, err := s.Get(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, DeviceAuthorizationStatePending, row.State)
	assert.Empty(t, row.UserID)

	// Commit transitions cleanly to approved.
	res2, ok := s.ReserveApproveByUserCode("STP001", alice, now)
	require.True(t, ok)
	s.CommitApproveByUserCode(res2, ApproveDeviceAuthorizationParams{UserCode: "STP001", UserID: alice, AuthGeneration: 4})
	row, err = s.Get(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, DeviceAuthorizationStateApproved, row.State)
	assert.Equal(t, alice.String(), row.UserID)
	assert.Equal(t, int64(4), row.AuthGeneration)
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseInvalidateAndDelete(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("user-client invalidation blocks and invalidates in-flight reserve", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hAlice := testDeviceHash("alice-grant")
		hBob := testDeviceHash("bob-grant")
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hAlice, UserCode: "ALI001", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hBob, UserCode: "BOB001", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		_, err := s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "ALI001", UserID: alice}, now)
		require.NoError(t, err)
		_, err = s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "BOB001", UserID: bob}, now)
		require.NoError(t, err)

		res, ok := s.ReserveConsume(hAlice, now)
		require.True(t, ok)

		inv := s.BeginInvalidateForUserClient("app-1", alice)
		assert.True(t, s.IsInvalidated(res), "in-flight reservation must see active invalidation guard")

		// Abort preserves grant and clears invalidation guard.
		s.AbortInvalidate(inv)
		assert.False(t, s.IsInvalidated(res))

		// Commit marks in-flight reservation invalidated and stamps ConsumedAt; ReleaseConsume cannot revive it.
		inv2 := s.BeginInvalidateForUserClient("app-1", alice)
		s.CommitInvalidate(inv2, now)
		assert.True(t, s.IsInvalidated(res))
		s.ReleaseConsume(res)

		rowAlice, err := s.Get(ctx, hAlice)
		require.NoError(t, err)
		require.NotNil(t, rowAlice.ConsumedAt)
		_, retryOK := s.ReserveConsume(hAlice, now)
		assert.False(t, retryOK, "ReleaseConsume must not revive an invalidated grant")

		rowBob, err := s.Get(ctx, hBob)
		require.NoError(t, err)
		assert.Nil(t, rowBob.ConsumedAt, "another user's grant must stay untouched")
	})

	t.Run("client invalidation stamps ConsumedAt and client delete removes entries", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hRevoke := testDeviceHash("revoke-client")
		hDelete := testDeviceHash("delete-client")
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hRevoke, UserCode: "REV001", ClientID: "app-rev", ExpiresAt: now.Add(time.Hour),
		}))
		_, err := s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{UserCode: "REV001", UserID: alice}, now)
		require.NoError(t, err)
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: hDelete, UserCode: "DEL001", ClientID: "app-del", ExpiresAt: now.Add(time.Hour),
		}))

		// Abort preserves.
		tokRevAbort := s.BeginInvalidateForClient("app-rev")
		_, blocked := s.ReserveConsume(hRevoke, now)
		assert.False(t, blocked, "active client invalidation must block ReserveConsume")
		s.AbortInvalidate(tokRevAbort)

		// Commit for RevokeApp stamps ConsumedAt.
		tokRev := s.BeginInvalidateForClient("app-rev")
		s.CommitInvalidate(tokRev, now)
		rowRev, err := s.Get(ctx, hRevoke)
		require.NoError(t, err)
		require.NotNil(t, rowRev.ConsumedAt)

		// Abort for DeleteApp preserves; Commit deletes entry.
		tokDelAbort := s.BeginDeleteForClient("app-del")
		s.AbortInvalidate(tokDelAbort)
		_, err = s.Get(ctx, hDelete)
		require.NoError(t, err)

		tokDel := s.BeginDeleteForClient("app-del")
		s.CommitInvalidate(tokDel, now)
		_, err = s.Get(ctx, hDelete)
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.GetByUserCode(ctx, "DEL001")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func TestMemoryDeviceAuthorizationStore_ConsumeApprovedForUserBeforeGeneration(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewMemoryDeviceAuthorizationStore()

	hGen1 := testDeviceHash("gen1")
	hGen1Reserved := testDeviceHash("gen1-reserved")
	hGen2 := testDeviceHash("gen2")

	for _, tc := range []struct {
		hash     []byte
		userCode string
		gen      int64
	}{
		{hGen1, "GEN101", 1},
		{hGen1Reserved, "GEN102", 1},
		{hGen2, "GEN201", 2},
	} {
		require.NoError(t, s.Create(ctx, CreateDeviceAuthorizationParams{
			DeviceCodeHash: tc.hash, UserCode: tc.userCode, ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		_, err := s.ApproveByUserCode(ctx, ApproveDeviceAuthorizationParams{
			UserCode: tc.userCode, UserID: alice, AuthGeneration: tc.gen,
		}, now)
		require.NoError(t, err)
	}

	resGen1, ok := s.ReserveConsume(hGen1Reserved, now)
	require.True(t, ok)

	n := s.ConsumeApprovedForUserBeforeGeneration(alice.String(), 2, now)
	assert.EqualValues(t, 2, n)
	assert.True(t, s.IsInvalidated(resGen1), "active reservation at older generation must report invalidated")
	s.ReleaseConsume(resGen1)

	r1, err := s.Get(ctx, hGen1)
	require.NoError(t, err)
	assert.NotNil(t, r1.ConsumedAt)

	r1Res, err := s.Get(ctx, hGen1Reserved)
	require.NoError(t, err)
	assert.NotNil(t, r1Res.ConsumedAt)

	r2, err := s.Get(ctx, hGen2)
	require.NoError(t, err)
	assert.Nil(t, r2.ConsumedAt, "grant approved at current generation must remain redeemable")
	resGen2, ok := s.ReserveConsume(hGen2, now)
	require.True(t, ok)
	s.CommitConsume(resGen2, now)
}
