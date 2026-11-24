//go:build linux

package procutil

import (
	"errors"
	"math"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInspectLinuxLiveProcessErrorHandling(t *testing.T) {
	t.Parallel()

	t.Run("ENOENT with EPERM reports alive without token under hidepid", func(t *testing.T) {
		t.Parallel()
		info := inspectLinuxLiveProcess(
			1234,
			func(string) ([]byte, error) { return nil, os.ErrNotExist },
			func(int) error { return syscall.EPERM },
		)
		assert.Equal(t, LiveProcessInfo{Alive: true}, info)
	})

	t.Run("ENOENT with ESRCH reports not alive", func(t *testing.T) {
		t.Parallel()
		info := inspectLinuxLiveProcess(
			1234,
			func(string) ([]byte, error) { return nil, os.ErrNotExist },
			func(int) error { return syscall.ESRCH },
		)
		assert.Equal(t, LiveProcessInfo{}, info)
	})

	t.Run("ESRCH from stat read with EPERM reports alive without token", func(t *testing.T) {
		t.Parallel()
		info := inspectLinuxLiveProcess(
			1234,
			func(string) ([]byte, error) { return nil, syscall.ESRCH },
			func(int) error { return syscall.EPERM },
		)
		assert.Equal(t, LiveProcessInfo{Alive: true}, info)
	})

	t.Run("unexpected read error reports alive conservatively without probing kill", func(t *testing.T) {
		t.Parallel()
		killCalled := false
		info := inspectLinuxLiveProcess(
			1234,
			func(string) ([]byte, error) { return nil, errors.New("io error") },
			func(int) error {
				killCalled = true
				return syscall.ESRCH
			},
		)
		assert.Equal(t, LiveProcessInfo{Alive: true}, info)
		assert.False(t, killCalled)
	})

	t.Run("invalid PIDs skip filesystem and kill calls", func(t *testing.T) {
		t.Parallel()
		called := false
		readStat := func(string) ([]byte, error) {
			called = true
			return nil, nil
		}
		killZero := func(int) error {
			called = true
			return nil
		}
		assert.Equal(t, LiveProcessInfo{}, inspectLinuxLiveProcess(0, readStat, killZero))
		assert.Equal(t, LiveProcessInfo{}, inspectLinuxLiveProcess(-1, readStat, killZero))
		if overflow := int64(math.MaxInt32) + 1; overflow <= math.MaxInt {
			assert.Equal(t, LiveProcessInfo{}, inspectLinuxLiveProcess(int(overflow), readStat, killZero))
		}
		assert.False(t, called)
	})
}
