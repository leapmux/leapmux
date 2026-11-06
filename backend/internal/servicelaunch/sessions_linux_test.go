//go:build linux

package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The linux monitor names the account's sessions from who, and a failed
// who is an error, never an empty list. These are the native
// session-translation cases the last-session rule consumes.
func TestLinuxSessionMonitor(t *testing.T) {
	m := newLinuxSessionMonitor()
	m.runWho = func() ([]byte, error) {
		return []byte("trustin tty1 2026-11-06 09:00\ntrustin pts/0 2026-11-06 09:15\nother tty2 2026-11-06 09:20\n"), nil
	}
	sessions, err := m.Sessions(AccountRef{ID: "1000", Name: "trustin"})
	require.NoError(t, err)
	assert.Len(t, sessions, 2, "only the account's sessions list")

	failing := newLinuxSessionMonitor()
	failing.runWho = func() ([]byte, error) { return nil, assert.AnError }
	_, err = failing.Sessions(AccountRef{ID: "1000", Name: "trustin"})
	require.Error(t, err)
}
