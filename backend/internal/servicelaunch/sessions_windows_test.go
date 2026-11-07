//go:build windows

package servicelaunch

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestSessionEnumerationUsesTheCurrentServer(t *testing.T) {
	var enumerations, releases int
	entries, err := enumerateSessionsWith(windowsSessionCalls{
		enumerate: func(server windows.Handle, reserved, version uint32, _ **windows.WTS_SESSION_INFO, count *uint32) error {
			enumerations++
			if server != 0 {
				return windows.ERROR_INVALID_HANDLE
			}
			assert.Zero(t, reserved)
			assert.EqualValues(t, 1, version)
			*count = 0
			return nil
		},
		free: func(pointer uintptr) {
			releases++
			assert.Zero(t, pointer)
		},
	})
	require.NoError(t, err, "the native API requires the null current-server handle")
	assert.Empty(t, entries)
	assert.Equal(t, 1, enumerations)
	assert.Zero(t, releases, "a null result transfers no native buffer")
}

func TestSessionEnumerationPreservesNativeFailures(t *testing.T) {
	_, err := enumerateSessionsWith(windowsSessionCalls{
		enumerate: func(windows.Handle, uint32, uint32, **windows.WTS_SESSION_INFO, *uint32) error {
			return windows.ERROR_ACCESS_DENIED
		},
		free: func(uintptr) { t.Fatal("a failed enumeration transfers no buffer") },
	})
	require.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
}

func TestSessionEnumerationValidatesAndReleasesNativeBuffers(t *testing.T) {
	entry := windows.WTS_SESSION_INFO{SessionID: 0}
	for _, test := range []struct {
		name    string
		info    *windows.WTS_SESSION_INFO
		count   uint32
		invalid bool
	}{
		{name: "empty"},
		{name: "allocated empty buffer", info: &entry},
		{name: "missing buffer", count: 1, invalid: true},
		{name: "excessive count", info: &entry, count: 1<<20 + 1, invalid: true},
		{name: "service session", info: &entry, count: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			releases := 0
			entries, err := enumerateSessionsWith(windowsSessionCalls{
				enumerate: func(_ windows.Handle, _, _ uint32, info **windows.WTS_SESSION_INFO, count *uint32) error {
					*info, *count = test.info, test.count
					return nil
				},
				free: func(pointer uintptr) {
					releases++
					assert.Equal(t, uintptr(unsafe.Pointer(test.info)), pointer)
				},
			})
			if test.invalid {
				require.ErrorContains(t, err, "unreadable session list")
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, entries)
			if test.info == nil {
				assert.Zero(t, releases)
			} else {
				assert.Equal(t, 1, releases, "every transferred native buffer is released exactly once")
			}
		})
	}
}
