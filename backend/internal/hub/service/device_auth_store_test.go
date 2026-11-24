package service

import (
	"bytes"
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
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("digest length, uniqueness, and pending defaults", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()

		for _, bad := range [][]byte{nil, {}, make([]byte, 31), make([]byte, 33)} {
			assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: bad, UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
			}), store.ErrInvalidArgument)
		}

		h1 := testDeviceHash("code-1")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h1, UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID,
			DeviceName: "laptop", RequestedScopes: "workspace:read", ExpiresAt: now.Add(time.Hour),
		}))

		// Duplicate deviceCodeHash and duplicate userCode both return ErrConflict.
		assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h1, UserCode: "ZZZZZZ", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}), store.ErrConflict)
		assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("code-2"), UserCode: "ABCDEF", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}), store.ErrConflict)

		row, ok := s.Get(h1)
		require.True(t, ok)
		assert.Equal(t, DeviceAuthorizationStatePending, row.State)
		assert.True(t, row.UserID.IsZero())
		assert.Empty(t, row.GrantedScopes)

		// Defensive clone on Get.
		row.DeviceCodeHash[0] ^= 0xff
		again, ok := s.Get(h1)
		require.True(t, ok)
		assert.True(t, bytes.Equal(h1, again.DeviceCodeHash))
	})

	t.Run("touch poll, approve, deny, consume, and retention until sweep", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hPending := testDeviceHash("pending")
		hDenied := testDeviceHash("denied")
		hApproved := testDeviceHash("approved")

		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hPending, UserCode: "PEND01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hDenied, UserCode: "DENY01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hApproved, UserCode: "APPR01", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))

		// Zero UserID refused on ApproveByUserCode.
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "PEND01"}, now)
		assert.False(t, approved)
		assert.ErrorIs(t, err, store.ErrInvalidArgument)

		// TouchPoll updates LastPolledAt.
		s.TouchPoll(hPending, now)
		got, ok := s.Get(hPending)
		require.True(t, ok)
		require.NotNil(t, got.LastPolledAt)
		assert.True(t, got.LastPolledAt.Equal(now))

		// Deny is final.
		assert.True(t, s.DenyByUserCode("DENY01"))
		assert.False(t, s.DenyByUserCode("DENY01"))
		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "DENY01", UserID: alice}, now)
		require.NoError(t, err)
		assert.False(t, approved)

		// Approve is single-use and rejects expired grants.
		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: alice}, now.Add(2*time.Hour))
		require.NoError(t, err)
		assert.False(t, approved)
		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: alice, GrantedScopes: "workspace:read", AuthGeneration: 3}, now)
		require.NoError(t, err)
		assert.True(t, approved)
		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "APPR01", UserID: bob, GrantedScopes: "admin:read"}, now)
		require.NoError(t, err)
		assert.False(t, approved)

		// Only approved, live grant is consumable; consumed row stays readable until DeleteExpired.
		assert.False(t, s.Consume(hPending, now))
		assert.False(t, s.Consume(hDenied, now))
		assert.False(t, s.Consume(hApproved, now.Add(time.Hour))) // at exact expiry: refused
		assert.True(t, s.Consume(hApproved, now))
		assert.False(t, s.Consume(hApproved, now))

		got, ok = s.Get(hApproved)
		require.True(t, ok, "consumed row must stay readable until DeleteExpired sweeps it")
		require.NotNil(t, got.ConsumedAt)
		assert.Equal(t, int64(3), got.AuthGeneration)
	})

	t.Run("delete expired boundaries and capacity cap lazy eviction", func(t *testing.T) {
		cur := now
		s := NewMemoryDeviceAuthorizationStore()
		s.maxEntries = 3
		s.now = func() time.Time { return cur }

		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-exp"), UserCode: "EXP001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-at"), UserCode: "ATC001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur,
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-live"), UserCode: "LIV001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Millisecond),
		}))

		// Strict < boundary: only d-exp is swept; d-at and d-live remain.
		assert.EqualValues(t, 1, s.DeleteExpired(cur))
		_, ok := s.Get(testDeviceHash("d-exp"))
		assert.False(t, ok)
		_, ok = s.Get(testDeviceHash("d-at"))
		assert.True(t, ok)
		_, ok = s.Get(testDeviceHash("d-live"))
		assert.True(t, ok)

		// Fill to capacity with live entries; 4th insert refused.
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-fill"), UserCode: "FIL001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}))
		assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-over"), UserCode: "OVR001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}), errDeviceAuthCapacity)

		// Advance clock past d-at and d-live: lazy eviction allows new insert.
		cur = cur.Add(time.Second)
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: testDeviceHash("d-over"), UserCode: "OVR001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: cur.Add(time.Hour),
		}))
	})
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseConsumeAndRollback(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewMemoryDeviceAuthorizationStore()

	h := testDeviceHash("two-phase-consume")
	require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
		DeviceCodeHash: h, UserCode: "TPC001", ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
	}))
	approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "TPC001", UserID: alice}, now)
	require.NoError(t, err)
	require.True(t, approved)

	res, ok := s.ReserveConsume(h, now)
	require.True(t, ok)
	assert.False(t, s.IsInvalidated(res))

	// Concurrent poller is blocked while reserved.
	_, ok2 := s.ReserveConsume(h, now)
	assert.False(t, ok2)

	// Release makes the grant retryable.
	s.ReleaseConsume(res)
	row, found := s.Get(h)
	require.True(t, found)
	assert.Nil(t, row.ConsumedAt)

	res2, ok := s.ReserveConsume(h, now)
	require.True(t, ok)
	s.CommitConsume(res2, now)

	row, found = s.Get(h)
	require.True(t, found)
	require.NotNil(t, row.ConsumedAt)
	_, ok3 := s.ReserveConsume(h, now)
	assert.False(t, ok3)
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseStepUpApproval(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewMemoryDeviceAuthorizationStore()

	h := testDeviceHash("stepup")
	require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
		DeviceCodeHash: h, UserCode: "STP001", ClientID: oauthapp.ControlCLIClientID,
		ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-1",
	}))

	res, ok := s.ReserveApproveByUserCode("STP001", alice, 4, now)
	require.True(t, ok)

	// Concurrent approve or deny is refused while reserved.
	_, ok2 := s.ReserveApproveByUserCode("STP001", alice, 4, now)
	assert.False(t, ok2)
	assert.False(t, s.DenyByUserCode("STP001"))

	// Release reverts cleanly to pending with empty UserID.
	s.ReleaseApproveByUserCode(res)
	row, found := s.Get(h)
	require.True(t, found)
	assert.Equal(t, DeviceAuthorizationStatePending, row.State)
	assert.True(t, row.UserID.IsZero())

	// Commit transitions cleanly to approved.
	res2, ok := s.ReserveApproveByUserCode("STP001", alice, 4, now)
	require.True(t, ok)
	s.CommitApproveByUserCode(res2, ApproveDeviceAuthorizationParams{UserCode: "STP001", UserID: alice, AuthGeneration: 4})
	row, found = s.Get(h)
	require.True(t, found)
	assert.Equal(t, DeviceAuthorizationStateApproved, row.State)
	assert.Equal(t, alice.String(), row.UserID.String())
	assert.Equal(t, int64(4), row.AuthGeneration)
}

func TestMemoryDeviceAuthorizationStore_StepUpApproveInvalidatedMidFlight(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")

	t.Run("user-client disconnect invalidates in-flight step-up approval", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		h := testDeviceHash("stepup-disconnect")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h, UserCode: "STP101", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-1",
		}))

		res, ok := s.ReserveApproveByUserCode("STP101", alice, 2, now)
		require.True(t, ok)

		inv := s.BeginInvalidateForUserClient("app-1", alice)
		s.CommitInvalidate(inv, now)

		s.CommitApproveByUserCode(res, ApproveDeviceAuthorizationParams{
			UserCode: "STP101", UserID: alice, AuthGeneration: 2,
		})

		row, found := s.Get(h)
		require.True(t, found)
		assert.NotEqual(t, DeviceAuthorizationStateApproved, row.State,
			"CommitApproveByUserCode must not approve a grant invalidated while reserved")
		assert.NotNil(t, row.ConsumedAt, "invalidated in-flight step-up grant must be marked consumed")
	})

	t.Run("user revocation generation invalidates in-flight step-up approval", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		h := testDeviceHash("stepup-revoked")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h, UserCode: "STP102", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-2",
		}))

		res, ok := s.ReserveApproveByUserCode("STP102", alice, 2, now)
		require.True(t, ok)

		s.ConsumeApprovedForUserBeforeGeneration(alice, 3, now)

		s.CommitApproveByUserCode(res, ApproveDeviceAuthorizationParams{
			UserCode: "STP102", UserID: alice, AuthGeneration: 2,
		})

		row, found := s.Get(h)
		require.True(t, found)
		assert.NotEqual(t, DeviceAuthorizationStateApproved, row.State,
			"CommitApproveByUserCode must not approve a grant whose auth generation was revoked while reserved")
		assert.NotNil(t, row.ConsumedAt, "revoked in-flight step-up grant must be marked consumed")
	})
}

func TestMemoryDeviceAuthorizationStore_TwoPhaseInvalidateAndDelete(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("user-client invalidation blocks and invalidates in-flight reserve", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hAlice := testDeviceHash("alice-grant")
		hBob := testDeviceHash("bob-grant")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hAlice, UserCode: "ALI001", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hBob, UserCode: "BOB001", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "ALI001", UserID: alice}, now)
		require.NoError(t, err)
		require.True(t, approved)
		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "BOB001", UserID: bob}, now)
		require.NoError(t, err)
		require.True(t, approved)

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

		rowAlice, found := s.Get(hAlice)
		require.True(t, found)
		require.NotNil(t, rowAlice.ConsumedAt)
		_, retryOK := s.ReserveConsume(hAlice, now)
		assert.False(t, retryOK, "ReleaseConsume must not revive an invalidated grant")

		rowBob, found := s.Get(hBob)
		require.True(t, found)
		assert.Nil(t, rowBob.ConsumedAt, "another user's grant must stay untouched")
	})

	t.Run("client invalidation stamps ConsumedAt and client delete removes entries", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hRevoke := testDeviceHash("revoke-client")
		hDelete := testDeviceHash("delete-client")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hRevoke, UserCode: "REV001", ClientID: "app-rev", ExpiresAt: now.Add(time.Hour),
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{UserCode: "REV001", UserID: alice}, now)
		require.NoError(t, err)
		require.True(t, approved)
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
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
		rowRev, found := s.Get(hRevoke)
		require.True(t, found)
		require.NotNil(t, rowRev.ConsumedAt)

		// Abort for DeleteApp preserves; Commit deletes entry.
		tokDelAbort := s.BeginDeleteForClient("app-del")
		s.AbortInvalidate(tokDelAbort)
		_, found = s.Get(hDelete)
		assert.True(t, found)

		tokDel := s.BeginDeleteForClient("app-del")
		s.CommitInvalidate(tokDel, now)
		_, found = s.Get(hDelete)
		assert.False(t, found)
		_, found = s.GetByUserCode("DEL001")
		assert.False(t, found)
	})
}

func TestMemoryDeviceAuthorizationStore_ConsumeApprovedForUserBeforeGeneration(t *testing.T) {
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
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: tc.hash, UserCode: tc.userCode, ClientID: oauthapp.ControlCLIClientID, ExpiresAt: now.Add(time.Hour),
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: tc.userCode, UserID: alice, AuthGeneration: tc.gen,
		}, now)
		require.NoError(t, err)
		require.True(t, approved)
	}

	resGen1, ok := s.ReserveConsume(hGen1Reserved, now)
	require.True(t, ok)

	assert.Zero(t, s.ConsumeApprovedForUserBeforeGeneration(userid.UserID{}, 2, now), "zero user ID must evict nothing")

	n := s.ConsumeApprovedForUserBeforeGeneration(alice, 2, now)
	assert.EqualValues(t, 2, n)
	assert.True(t, s.IsInvalidated(resGen1), "active reservation at older generation must report invalidated")
	s.ReleaseConsume(resGen1)

	r1, found := s.Get(hGen1)
	require.True(t, found)
	assert.NotNil(t, r1.ConsumedAt)

	r1Res, found := s.Get(hGen1Reserved)
	require.True(t, found)
	assert.NotNil(t, r1Res.ConsumedAt)

	r2, found := s.Get(hGen2)
	require.True(t, found)
	assert.Nil(t, r2.ConsumedAt, "grant approved at current generation must remain redeemable")
	resGen2, ok := s.ReserveConsume(hGen2, now)
	require.True(t, ok)
	s.CommitConsume(resGen2, now)
}

func TestMemoryDeviceAuthorizationStore_ActiveInvalidationGuardsApproval(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("user-client invalidation blocks only the target user until aborted", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		for _, tc := range []struct {
			label string
			code  string
		}{
			{"alice-direct", "GRD101"},
			{"alice-reserve", "GRD102"},
			{"bob-direct", "GRD103"},
			{"bob-reserve", "GRD104"},
		} {
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: testDeviceHash(tc.label),
				UserCode:       tc.code,
				ClientID:       "app-1",
				ExpiresAt:      now.Add(time.Hour),
			}))
		}

		inv := s.BeginInvalidateForUserClient("app-1", alice)

		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "GRD101", UserID: alice, GrantedScopes: "workspace:read",
		}, now)
		require.NoError(t, err)
		assert.False(t, approved, "active user-client invalidation must block ApproveByUserCode for that user")

		_, reserved := s.ReserveApproveByUserCode("GRD102", alice, 1, now)
		assert.False(t, reserved, "active user-client invalidation must block ReserveApproveByUserCode for that user")

		// Another user can still approve and reserve pending grants for the same client.
		bobApproved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "GRD103", UserID: bob, GrantedScopes: "workspace:read",
		}, now)
		require.NoError(t, err)
		assert.True(t, bobApproved, "active user-client invalidation must not block another user's ApproveByUserCode")

		bobRes, bobReserved := s.ReserveApproveByUserCode("GRD104", bob, 1, now)
		require.True(t, bobReserved, "active user-client invalidation must not block another user's ReserveApproveByUserCode")
		s.ReleaseApproveByUserCode(bobRes)

		// After AbortInvalidate, alice can approve and reserve again.
		s.AbortInvalidate(inv)

		approved, err = s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "GRD101", UserID: alice, GrantedScopes: "workspace:read",
		}, now)
		require.NoError(t, err)
		assert.True(t, approved)

		aliceRes, reserved := s.ReserveApproveByUserCode("GRD102", alice, 1, now)
		require.True(t, reserved)
		s.ReleaseApproveByUserCode(aliceRes)
	})

	for _, tc := range []struct {
		name  string
		begin func(*MemoryDeviceAuthorizationStore) DeviceInvalidateToken
	}{
		{
			name: "client invalidation blocks every user until aborted",
			begin: func(s *MemoryDeviceAuthorizationStore) DeviceInvalidateToken {
				return s.BeginInvalidateForClient("app-1")
			},
		},
		{
			name:  "client delete blocks every user until aborted",
			begin: func(s *MemoryDeviceAuthorizationStore) DeviceInvalidateToken { return s.BeginDeleteForClient("app-1") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryDeviceAuthorizationStore()
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: testDeviceHash("direct-" + tc.name),
				UserCode:       "CLG101",
				ClientID:       "app-1",
				ExpiresAt:      now.Add(time.Hour),
			}))
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: testDeviceHash("reserve-" + tc.name),
				UserCode:       "CLG102",
				ClientID:       "app-1",
				ExpiresAt:      now.Add(time.Hour),
			}))

			inv := tc.begin(s)

			for _, u := range []userid.UserID{alice, bob} {
				approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
					UserCode: "CLG101", UserID: u,
				}, now)
				require.NoError(t, err)
				assert.False(t, approved)

				_, reserved := s.ReserveApproveByUserCode("CLG102", u, 1, now)
				assert.False(t, reserved)
			}

			s.AbortInvalidate(inv)

			approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
				UserCode: "CLG101", UserID: alice,
			}, now)
			require.NoError(t, err)
			assert.True(t, approved)

			res, reserved := s.ReserveApproveByUserCode("CLG102", bob, 1, now)
			require.True(t, reserved)
			s.ReleaseApproveByUserCode(res)
		})
	}
}

func TestMemoryDeviceAuthorizationStore_ReleaseApproveOnInvalidatedReservation(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")

	for _, tc := range []struct {
		name       string
		invalidate func(s *MemoryDeviceAuthorizationStore)
	}{
		{
			name: "user-client invalidation committed before release",
			invalidate: func(s *MemoryDeviceAuthorizationStore) {
				inv := s.BeginInvalidateForUserClient("app-1", alice)
				s.CommitInvalidate(inv, now)
			},
		},
		{
			name: "user generation revocation before release",
			invalidate: func(s *MemoryDeviceAuthorizationStore) {
				s.ConsumeApprovedForUserBeforeGeneration(alice, 3, now)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryDeviceAuthorizationStore()
			h := testDeviceHash("release-inv-" + tc.name)
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h,
				UserCode:       "REL101",
				ClientID:       "app-1",
				ExpiresAt:      now.Add(time.Hour),
				ElevateTokenID: "tok-1",
			}))

			res, ok := s.ReserveApproveByUserCode("REL101", alice, 2, now)
			require.True(t, ok)

			tc.invalidate(s)
			s.ReleaseApproveByUserCode(res)

			row, found := s.Get(h)
			require.True(t, found)
			assert.Equal(t, DeviceAuthorizationStatePending, row.State)
			assert.NotNil(t, row.ConsumedAt, "releasing an invalidated approve reservation must stamp ConsumedAt")

			_, retryReserve := s.ReserveApproveByUserCode("REL101", alice, 3, now)
			assert.False(t, retryReserve, "a consumed pending grant must refuse later ReserveApproveByUserCode")

			retryApprove, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
				UserCode: "REL101", UserID: alice, AuthGeneration: 3,
			}, now)
			require.NoError(t, err)
			assert.False(t, retryApprove, "a consumed pending grant must refuse later ApproveByUserCode")
		})
	}
}

func TestMemoryDeviceAuthorizationStore_StepUpSelectivityAndClientDeletionWhileReserved(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("generation revocation preserves current-generation and other-user reservations", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hAlice := testDeviceHash("sel-alice-gen3")
		hBob := testDeviceHash("sel-bob-gen2")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hAlice, UserCode: "SEL101", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-a",
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hBob, UserCode: "SEL102", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-b",
		}))

		resAlice, ok := s.ReserveApproveByUserCode("SEL101", alice, 3, now)
		require.True(t, ok)
		resBob, ok := s.ReserveApproveByUserCode("SEL102", bob, 2, now)
		require.True(t, ok)

		s.ConsumeApprovedForUserBeforeGeneration(alice, 3, now)

		s.CommitApproveByUserCode(resAlice, ApproveDeviceAuthorizationParams{
			UserCode: "SEL101", UserID: alice, AuthGeneration: 3,
		})
		s.CommitApproveByUserCode(resBob, ApproveDeviceAuthorizationParams{
			UserCode: "SEL102", UserID: bob, AuthGeneration: 2,
		})

		rowAlice, found := s.Get(hAlice)
		require.True(t, found)
		assert.Equal(t, DeviceAuthorizationStateApproved, rowAlice.State)
		assert.Nil(t, rowAlice.ConsumedAt)

		rowBob, found := s.Get(hBob)
		require.True(t, found)
		assert.Equal(t, DeviceAuthorizationStateApproved, rowBob.State)
		assert.Nil(t, rowBob.ConsumedAt)
	})

	t.Run("user-client invalidation for alice preserves bob's step-up reservation", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hBob := testDeviceHash("sel-bob-userclient")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hBob, UserCode: "SEL201", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-b",
		}))

		resBob, ok := s.ReserveApproveByUserCode("SEL201", bob, 2, now)
		require.True(t, ok)

		inv := s.BeginInvalidateForUserClient("app-1", alice)
		s.CommitInvalidate(inv, now)

		s.CommitApproveByUserCode(resBob, ApproveDeviceAuthorizationParams{
			UserCode: "SEL201", UserID: bob, AuthGeneration: 2,
		})

		rowBob, found := s.Get(hBob)
		require.True(t, found)
		assert.Equal(t, DeviceAuthorizationStateApproved, rowBob.State)
		assert.Nil(t, rowBob.ConsumedAt)
	})

	for _, action := range []string{"commit", "release"} {
		t.Run("client deletion while reserved does not panic on "+action, func(t *testing.T) {
			s := NewMemoryDeviceAuthorizationStore()
			h := testDeviceHash("del-reserved-" + action)
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h, UserCode: "DEL301", ClientID: "app-del",
				ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-d",
			}))

			res, ok := s.ReserveApproveByUserCode("DEL301", alice, 2, now)
			require.True(t, ok)

			inv := s.BeginDeleteForClient("app-del")
			s.CommitInvalidate(inv, now)

			assert.NotPanics(t, func() {
				if action == "commit" {
					s.CommitApproveByUserCode(res, ApproveDeviceAuthorizationParams{
						UserCode: "DEL301", UserID: alice, AuthGeneration: 2,
					})
				} else {
					s.ReleaseApproveByUserCode(res)
				}
			})

			_, found := s.Get(h)
			assert.False(t, found)
			_, found = s.GetByUserCode("DEL301")
			assert.False(t, found)
		})
	}
}

func TestMemoryDeviceAuthorizationStore_EvictionTrackingWithReservationsAndOutOfOrderExpiry(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")

	for _, finish := range []string{"release", "commit"} {
		t.Run("noteEvictableLocked after consume reservation "+finish, func(t *testing.T) {
			cur := now
			s := NewMemoryDeviceAuthorizationStore()
			s.maxEntries = 2
			s.now = func() time.Time { return cur }

			h1 := testDeviceHash("evict-consume-1-" + finish)
			h2 := testDeviceHash("evict-consume-2-" + finish)
			h3 := testDeviceHash("evict-consume-3-" + finish)
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h1, UserCode: "EVC001", ClientID: "app-1", ExpiresAt: cur.Add(time.Second),
			}))
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h2, UserCode: "EVC002", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}))
			approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
				UserCode: "EVC001", UserID: alice,
			}, cur)
			require.NoError(t, err)
			require.True(t, approved)

			res, ok := s.ReserveConsume(h1, cur)
			require.True(t, ok)

			// Advance past E1's expiry while E1 is still reserved: sweep skips E1 and raises minExpiresAt to E2.
			cur = cur.Add(2 * time.Second)
			assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h3, UserCode: "EVC003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}), errDeviceAuthCapacity)

			// Releasing or committing the reservation restores minExpiresAt so Create immediately evicts E1.
			if finish == "release" {
				s.ReleaseConsume(res)
			} else {
				s.CommitConsume(res, cur)
			}

			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h3, UserCode: "EVC003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}))
			_, ok = s.Get(h1)
			assert.False(t, ok, "expired entry must be evicted once its consume reservation ends")
			_, ok = s.Get(h2)
			assert.True(t, ok)
			_, ok = s.Get(h3)
			assert.True(t, ok)
		})

		t.Run("noteEvictableLocked after approve reservation "+finish, func(t *testing.T) {
			cur := now
			s := NewMemoryDeviceAuthorizationStore()
			s.maxEntries = 2
			s.now = func() time.Time { return cur }

			h1 := testDeviceHash("evict-approve-1-" + finish)
			h2 := testDeviceHash("evict-approve-2-" + finish)
			h3 := testDeviceHash("evict-approve-3-" + finish)
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h1, UserCode: "EVA001", ClientID: "app-1", ExpiresAt: cur.Add(time.Second),
			}))
			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h2, UserCode: "EVA002", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}))

			res, ok := s.ReserveApproveByUserCode("EVA001", alice, 1, cur)
			require.True(t, ok)

			cur = cur.Add(2 * time.Second)
			assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h3, UserCode: "EVA003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}), errDeviceAuthCapacity)

			if finish == "release" {
				s.ReleaseApproveByUserCode(res)
			} else {
				s.CommitApproveByUserCode(res, ApproveDeviceAuthorizationParams{
					UserCode: "EVA001", UserID: alice, AuthGeneration: 1,
				})
			}

			require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
				DeviceCodeHash: h3, UserCode: "EVA003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
			}))
			_, ok = s.Get(h1)
			assert.False(t, ok, "expired entry must be evicted once its approve reservation ends")
			_, ok = s.Get(h2)
			assert.True(t, ok)
			_, ok = s.Get(h3)
			assert.True(t, ok)
		})
	}

	t.Run("out-of-order expiry insertion updates minExpiresAt", func(t *testing.T) {
		cur := now
		s := NewMemoryDeviceAuthorizationStore()
		s.maxEntries = 2
		s.now = func() time.Time { return cur }

		h1 := testDeviceHash("ooo-1")
		h2 := testDeviceHash("ooo-2")
		h3 := testDeviceHash("ooo-3")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h1, UserCode: "OOO001", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h2, UserCode: "OOO002", ClientID: "app-1", ExpiresAt: cur.Add(time.Second),
		}))

		// At exact E2 expiry, strict < keeps E2 alive and Create hits capacity.
		cur = now.Add(time.Second)
		assert.ErrorIs(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h3, UserCode: "OOO003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
		}), errDeviceAuthCapacity)

		// Past E2 expiry, Create evicts E2 and keeps E1.
		cur = now.Add(2 * time.Second)
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h3, UserCode: "OOO003", ClientID: "app-1", ExpiresAt: cur.Add(time.Hour),
		}))
		_, ok := s.Get(h2)
		assert.False(t, ok)
		_, ok = s.Get(h1)
		assert.True(t, ok)
		_, ok = s.Get(h3)
		assert.True(t, ok)
	})
}

func TestDeviceTwoPhaseHelpers(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	clock := func() time.Time { return now }

	t.Run("withDeviceInvalidation aborts guard on panic", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		h := testDeviceHash("helper-inv-panic")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: h, UserCode: "HLP101", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "HLP101", UserID: alice,
		}, now)
		require.NoError(t, err)
		require.True(t, approved)

		assert.Panics(t, func() {
			_ = withDeviceInvalidation(s, s.BeginInvalidateForClient("app-1"), clock, func() error {
				panic("simulated panic during invalidation")
			})
		})

		row, found := s.Get(h)
		require.True(t, found)
		assert.Nil(t, row.ConsumedAt, "a panic inside withDeviceInvalidation must not commit invalidation")
		res, ok := s.ReserveConsume(h, now)
		require.True(t, ok, "AbortInvalidate on panic must unblock ReserveConsume")
		s.ReleaseConsume(res)
	})

	t.Run("withDeviceConsume releases on panic and rejects unconsumable grants without running fn", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hApproved := testDeviceHash("helper-consume-approved")
		hPending := testDeviceHash("helper-consume-pending")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hApproved, UserCode: "HLP201", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hPending, UserCode: "HLP202", ClientID: "app-1", ExpiresAt: now.Add(time.Hour),
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "HLP201", UserID: alice,
		}, now)
		require.NoError(t, err)
		require.True(t, approved)

		assert.Panics(t, func() {
			_ = withDeviceConsume(s, hApproved, now, clock, func(func() bool) error {
				panic("simulated panic during consume")
			})
		})

		row, found := s.Get(hApproved)
		require.True(t, found)
		assert.Nil(t, row.ConsumedAt)
		res, ok := s.ReserveConsume(hApproved, now)
		require.True(t, ok, "ReleaseConsume on panic must make the grant reservable again")
		s.ReleaseConsume(res)

		for _, tc := range []struct {
			name string
			hash []byte
		}{
			{"pending grant", hPending},
			{"unknown hash", testDeviceHash("helper-consume-unknown")},
		} {
			called := false
			err := withDeviceConsume(s, tc.hash, now, clock, func(func() bool) error {
				called = true
				return nil
			})
			assert.ErrorIs(t, err, errAuthorizationGrantUnavailable, tc.name)
			assert.False(t, called, "fn must not run for %s", tc.name)
		}
	})

	t.Run("withDeviceStepUpApprove releases on panic and rejects unapprovable codes without running fn", func(t *testing.T) {
		s := NewMemoryDeviceAuthorizationStore()
		hPending := testDeviceHash("helper-stepup-pending")
		hApproved := testDeviceHash("helper-stepup-approved")
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hPending, UserCode: "HLP301", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-1",
		}))
		require.NoError(t, s.Create(CreateDeviceAuthorizationParams{
			DeviceCodeHash: hApproved, UserCode: "HLP302", ClientID: "app-1",
			ExpiresAt: now.Add(time.Hour), ElevateTokenID: "tok-2",
		}))
		approved, err := s.ApproveByUserCode(ApproveDeviceAuthorizationParams{
			UserCode: "HLP302", UserID: alice, AuthGeneration: 1,
		}, now)
		require.NoError(t, err)
		require.True(t, approved)

		assert.Panics(t, func() {
			_ = withDeviceStepUpApprove(s, ApproveDeviceAuthorizationParams{
				UserCode: "HLP301", UserID: alice, AuthGeneration: 1,
			}, now, func() error {
				panic("simulated panic during step-up approve")
			})
		})

		row, found := s.Get(hPending)
		require.True(t, found)
		assert.Equal(t, DeviceAuthorizationStatePending, row.State)
		res, ok := s.ReserveApproveByUserCode("HLP301", alice, 1, now)
		require.True(t, ok, "ReleaseApproveByUserCode on panic must make the grant reservable again")
		s.ReleaseApproveByUserCode(res)

		for _, tc := range []struct {
			name string
			code string
		}{
			{"already approved", "HLP302"},
			{"unknown user code", "NOPE99"},
		} {
			called := false
			err := withDeviceStepUpApprove(s, ApproveDeviceAuthorizationParams{
				UserCode: tc.code, UserID: alice, AuthGeneration: 1,
			}, now, func() error {
				called = true
				return nil
			})
			assert.ErrorIs(t, err, errDeviceGrantNotApprovable, tc.name)
			assert.False(t, called, "fn must not run for %s", tc.name)
		}
	})
}
