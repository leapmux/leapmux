//go:build windows

package procutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckNoJobRequiresVerifiedAbsenceOfEveryJob(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		inJob   bool
		err     error
		allowed bool
	}{
		{allowed: true},
		{inJob: true},
		{err: assert.AnError},
		{inJob: true, err: assert.AnError},
	} {
		calls := 0
		err := validateWindowsProcessJob(func() (bool, error) { calls++; return tc.inJob, tc.err })
		assert.Equal(t, tc.allowed, err == nil)
		assert.Equal(t, 1, calls)
	}
}
