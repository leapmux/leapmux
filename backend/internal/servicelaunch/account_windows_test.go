//go:build windows

package servicelaunch

import (
	"os/user"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestResolveWindowsAccountNativeSpellings(t *testing.T) {
	account, err := user.Current()
	require.NoError(t, err)
	for _, name := range []string{account.Username, account.Uid} {
		resolved, err := resolveWindowsAccount(name)
		require.NoError(t, err, name)
		assert.Equal(t, account.Uid, resolved)
	}
	t.Run("local account prefix", func(t *testing.T) {
		computer, err := windows.ComputerName()
		require.NoError(t, err)
		domain, local, qualified := strings.Cut(account.Username, `\`)
		if !qualified || !strings.EqualFold(domain, computer) {
			t.Skip("the current account is not a local Windows account; native local-prefix lookup requires that fixture")
		}
		resolved, err := resolveWindowsAccount(`.\` + local)
		require.NoError(t, err)
		assert.Equal(t, account.Uid, resolved)
	})
	for _, name := range []string{"LocalSystem", "localsystem", `NT AUTHORITY\SYSTEM`} {
		resolved, err := resolveWindowsAccount(name)
		require.NoError(t, err)
		assert.Equal(t, "S-1-5-18", resolved)
	}
	for _, name := range []string{"", "bad\x00name", `.\LeapMux-No-Such-Account-38b683dd`} {
		_, err := resolveWindowsAccount(name)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Windows account")
	}
}
