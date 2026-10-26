//go:build unix

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequirePowerShellArgumentPassing prepares a private Unix profile that selects
// mode and reports it through modeEnv. The caller passes home as HOME and
// XDG_CONFIG_HOME, so the shell reads this profile instead of the user's.
func RequirePowerShellArgumentPassing(t *testing.T, mode, modeEnv string) (shell, home string) {
	t.Helper()
	require.Contains(t, []string{"Legacy", "Standard"}, mode)
	require.Regexp(t, `^[A-Za-z_][A-Za-z0-9_]*$`, modeEnv)
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is not installed")
	}
	home = t.TempDir()
	profile := filepath.Join(home, "powershell", "Microsoft.PowerShell_profile.ps1")
	require.NoError(t, os.MkdirAll(filepath.Dir(profile), 0o700))
	// Reporting the selected mode makes a test fail if PowerShell stops reading
	// this profile, instead of silently testing its default argument passing.
	require.NoError(t, os.WriteFile(profile, []byte(
		"$PSNativeCommandArgumentPassing = '"+mode+"'\n"+
			"$env:"+modeEnv+" = \"$PSNativeCommandArgumentPassing\"\n"), 0o600))
	return shell, home
}
