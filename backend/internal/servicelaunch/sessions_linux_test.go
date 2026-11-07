//go:build linux

package servicelaunch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Native graphical sessions count; shell sessions and other accounts do not.
func TestLinuxSessionMonitor(t *testing.T) {
	m := newLinuxSessionMonitor()
	m.runLoginctl = func(_ context.Context, args []string) ([]byte, error) {
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
	sessions, err := m.Sessions(t.Context(), AccountRef{ID: "1000", Name: "trustin"})
	require.NoError(t, err)
	assert.Len(t, sessions, 2, "only the account's sessions list")

	failing := newLinuxSessionMonitor()
	failing.runLoginctl = func(context.Context, []string) ([]byte, error) { return nil, assert.AnError }
	_, err = failing.Sessions(t.Context(), AccountRef{ID: "1000", Name: "trustin"})
	require.Error(t, err)
}

func TestLinuxSessionDetailsExcludeForeignAccounts(t *testing.T) {
	for _, test := range []struct { rows string; own bool; failure bool }{
		{"1 1001 other\n2 1002 another\n", false, false},
		{"1 1001 other\n2 0 root\n", true, false},
		{" \n\t\n", false, false},
		{"truncated\n", false, true},
		{"1 invalid user\n", false, true},
		{"1 -1 user\n", false, true},
		{"1 999999999999999999999 user\n", false, true},
	} {
		t.Run(test.rows, func(t *testing.T) {
			monitor := newLinuxSessionMonitor()
			calls := 0
			var operation context.Context
			monitor.runLoginctl = func(ctx context.Context, args []string) ([]byte, error) {
				calls++
				if args[0] == "list-sessions" { operation = ctx; return []byte(test.rows), nil }
				require.Same(t, operation, ctx, "all queries share one deadline")
				require.Equal(t, "2", args[1], "foreign session details must not be queried")
				return []byte("User=0\nType=wayland\nClass=user\nState=active\n"), nil
			}
			sessions, err := monitor.Sessions(t.Context(), AccountRef{ID: "0", Name: "root"})
			if test.failure { require.Error(t, err); return }
			require.NoError(t, err)
			if test.own { assert.Len(t, sessions, 1); assert.Equal(t, 2, calls) } else { assert.Empty(t, sessions); assert.Equal(t, 1, calls) }
		})
	}
}

func TestLinuxSessionQueryStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	monitor := newLinuxSessionMonitor()
	calls := 0
	monitor.runLoginctl = func(context.Context, []string) ([]byte, error) {
		calls++
		cancel()
		return []byte("1 1000 user\n"), nil
	}
	_, err := monitor.Sessions(ctx, AccountRef{ID: "1000"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}
