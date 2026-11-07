//go:build linux

package servicelaunch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Native graphical sessions count; shell sessions and other accounts do not.
func TestLinuxSessionMonitor(t *testing.T) {
	m := newLinuxSessionMonitor()
	m.runLoginctl = func(args []string) ([]byte, error) {
		if args[0] == "list-sessions" {
			return []byte("1 1000 trustin\n2 1000 trustin\n3 1001 other\n4 1000 trustin\n"), nil
		}
		switch args[1] {
		case "1":
			return []byte("User=1000\nType=wayland\nClass=user\nState=active\n"), nil
		case "2":
			return []byte("User=1000\nType=tty\nClass=user\nState=active\n"), nil
		case "3":
			return []byte("User=1001\nType=x11\nClass=user\nState=active\n"), nil
		default:
			return []byte("User=1000\nType=x11\nClass=user\nState=online\n"), nil
		}
	}
	sessions, err := m.Sessions(AccountRef{ID: "1000", Name: "trustin"})
	require.NoError(t, err)
	assert.Len(t, sessions, 2, "only the account's sessions list")

	failing := newLinuxSessionMonitor()
	failing.runLoginctl = func([]string) ([]byte, error) { return nil, assert.AnError }
	_, err = failing.Sessions(AccountRef{ID: "1000", Name: "trustin"})
	require.Error(t, err)
}
