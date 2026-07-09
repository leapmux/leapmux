package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestPreTouchPollOAuthError_ExpiresAtBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	h := &APIAuthHandler{}

	code, _, rejected := h.preTouchPollOAuthError(&store.DeviceAuthorization{ExpiresAt: now}, now)

	require.True(t, rejected)
	assert.Equal(t, "expired_token", code)
}

func TestPreTouchPollOAuthError_ThrottleUsesSuppliedNow(t *testing.T) {
	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	lastPoll := now.Add(-time.Second)
	h := &APIAuthHandler{}
	row := &store.DeviceAuthorization{
		ExpiresAt:       now.Add(time.Hour),
		LastPolledAt:    &lastPoll,
		IntervalSeconds: 5,
	}

	code, _, rejected := h.preTouchPollOAuthError(row, now)

	require.True(t, rejected)
	assert.Equal(t, "slow_down", code)
}
