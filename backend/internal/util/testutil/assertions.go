package testutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertEventually is a convenience wrapper around assert.Eventually
// with standardized timeout (10s) and polling interval (10ms).
func AssertEventually(t *testing.T, condition func() bool, msgAndArgs ...interface{}) bool {
	t.Helper()
	return assert.Eventually(t, condition, 10*time.Second, 10*time.Millisecond, msgAndArgs...)
}

// RequireEventually is a convenience wrapper around require.Eventually
// with standardized timeout (10s) and polling interval (10ms).
func RequireEventually(t *testing.T, condition func() bool, msgAndArgs ...interface{}) {
	t.Helper()
	require.Eventually(t, condition, 10*time.Second, 10*time.Millisecond, msgAndArgs...)
}
