package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthGateReduced(t *testing.T) {
	base := UserInfoCacheFields{
		Username:      "u",
		Email:         "u@example.com",
		EmailVerified: false,
		IsAdmin:       false,
	}

	t.Run("admin true to false", func(t *testing.T) {
		before := base
		before.IsAdmin = true
		after := base
		assert.True(t, AuthGateReduced(before, after))
	})

	t.Run("email verified true to false", func(t *testing.T) {
		before := base
		before.EmailVerified = true
		after := base
		assert.True(t, AuthGateReduced(before, after))
	})

	t.Run("admin false to true", func(t *testing.T) {
		before := base
		after := base
		after.IsAdmin = true
		assert.False(t, AuthGateReduced(before, after))
	})

	t.Run("email verified false to true", func(t *testing.T) {
		before := base
		after := base
		after.EmailVerified = true
		assert.False(t, AuthGateReduced(before, after))
	})

	t.Run("both gates stay false", func(t *testing.T) {
		assert.False(t, AuthGateReduced(base, base))
	})

	t.Run("both gates stay true", func(t *testing.T) {
		before := base
		before.IsAdmin = true
		before.EmailVerified = true
		after := before
		assert.False(t, AuthGateReduced(before, after))
	})

	t.Run("unrelated field change", func(t *testing.T) {
		before := base
		before.IsAdmin = true
		before.EmailVerified = true
		after := before
		after.Username = "renamed"
		after.Email = "new@example.com"
		assert.False(t, AuthGateReduced(before, after))
	})
}

type fakeConn struct{}

func TestRunUserInfoMutationRouting(t *testing.T) {
	type outcome struct {
		emitted bool
		fenced  bool
	}

	runSeq := func(
		t *testing.T,
		before, after UserInfoCacheFields,
		existedBefore, existedAfter bool,
		fenceEnabled bool,
	) outcome {
		t.Helper()
		var out outcome
		loads := 0
		var fence func(context.Context, fakeConn, string, time.Time) error
		if fenceEnabled {
			fence = func(context.Context, fakeConn, string, time.Time) error {
				out.fenced = true
				return nil
			}
		}
		err := RunUserInfoMutation(context.Background(),
			func(_ context.Context, fn func(fakeConn) error) error {
				return fn(fakeConn{})
			},
			func(context.Context, fakeConn) (UserInfoCacheFields, bool, error) {
				loads++
				if loads == 1 {
					return before, existedBefore, nil
				}
				return after, existedAfter, nil
			},
			func(context.Context, fakeConn) (string, time.Time, bool, error) {
				return "user-1", time.Unix(1, 0).UTC(), true, nil
			},
			func(context.Context, fakeConn, string, time.Time) error {
				out.emitted = true
				return nil
			},
			fence,
		)
		require.NoError(t, err)
		assert.Equal(t, 2, loads)
		return out
	}

	gateOn := UserInfoCacheFields{IsAdmin: true, EmailVerified: true}
	gateOff := UserInfoCacheFields{}
	grantAdmin := UserInfoCacheFields{IsAdmin: true}
	usernameChange := UserInfoCacheFields{Username: "renamed", IsAdmin: true, EmailVerified: true}

	t.Run("fence on gate reduction when fence enabled", func(t *testing.T) {
		out := runSeq(t, gateOn, gateOff, true, true, true)
		assert.True(t, out.fenced)
		assert.False(t, out.emitted)
	})

	t.Run("emit on grant when fence enabled", func(t *testing.T) {
		out := runSeq(t, gateOff, grantAdmin, true, true, true)
		assert.True(t, out.emitted)
		assert.False(t, out.fenced)
	})

	t.Run("emit on non-gate change when fence enabled", func(t *testing.T) {
		out := runSeq(t, gateOn, usernameChange, true, true, true)
		assert.True(t, out.emitted)
		assert.False(t, out.fenced)
	})

	t.Run("neither on no-op", func(t *testing.T) {
		out := runSeq(t, gateOn, gateOn, true, true, true)
		assert.False(t, out.emitted)
		assert.False(t, out.fenced)
	})

	t.Run("emit not fence when fence nil even on reduction", func(t *testing.T) {
		out := runSeq(t, gateOn, gateOff, true, true, false)
		assert.True(t, out.emitted)
		assert.False(t, out.fenced)
	})

	t.Run("emit on existence flip even with fence", func(t *testing.T) {
		out := runSeq(t, gateOn, gateOff, true, false, true)
		assert.True(t, out.emitted)
		assert.False(t, out.fenced)
	})
}
