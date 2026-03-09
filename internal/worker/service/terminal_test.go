package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTitleDebouncer(t *testing.T) {
	d := newTitleDebouncer(100 * time.Millisecond)

	// First call should always be allowed.
	assert.True(t, d.shouldSave("t1"), "first call should be allowed")

	// Immediate second call should be rejected.
	assert.False(t, d.shouldSave("t1"), "immediate second call should be rejected")

	// Different terminal should be independent.
	assert.True(t, d.shouldSave("t2"), "different terminal should be independent")

	// After the interval, should be allowed again.
	time.Sleep(110 * time.Millisecond)
	assert.True(t, d.shouldSave("t1"), "call after interval should be allowed")
}
