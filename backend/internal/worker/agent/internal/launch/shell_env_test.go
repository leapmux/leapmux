package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileShell writes a shell that exports AMP_SETTINGS_FILE, as a profile
// would, and then runs /bin/sh with its own arguments.
func profileShell(t *testing.T, exports string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shebang stub cannot exec on Windows")
	}
	shell := filepath.Join(t.TempDir(), "sh")
	require.NoError(t, os.WriteFile(shell, []byte("#!/bin/sh\n"+exports+"\nexec /bin/sh \"$@\"\n"), 0o755))
	return shell
}

// The probe reads the values that the shell sets up, not the worker's own.
func TestShellEnvReadsTheShellsValues(t *testing.T) {
	t.Setenv("LEAPMUX_PROBE_WORKER_ONLY", "from-the-worker")
	shell := profileShell(t, "export LEAPMUX_PROBE_FILE='/a path/with = signs'\nunset LEAPMUX_PROBE_WORKER_ONLY")

	values, result := ShellEnv(context.Background(), ShellEnvOptions{Shell: shell, Variables: []string{"LEAPMUX_PROBE_FILE", "LEAPMUX_PROBE_WORKER_ONLY", "LEAPMUX_PROBE_UNSET"}})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{
		"LEAPMUX_PROBE_FILE":        "/a path/with = signs",
		"LEAPMUX_PROBE_WORKER_ONLY": "",
		"LEAPMUX_PROBE_UNSET":       "",
	}, values.Values)
}

// A value that holds a quote or a dollar sign reaches the caller as it is: the
// native child reads the variable without evaluating its value.
func TestShellEnvKeepsSpecialCharacters(t *testing.T) {
	shell := profileShell(t, `export LEAPMUX_PROBE_FILE='it'"'"'s $HOME/%s'`)
	values, result := ShellEnv(context.Background(), ShellEnvOptions{Shell: shell, Variables: []string{"LEAPMUX_PROBE_FILE"}})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, `it's $HOME/%s`, values.Values["LEAPMUX_PROBE_FILE"])
}

// A shell that exits before the probe establishes nothing, and so does one
// that cannot start.
func TestShellEnvInconclusive(t *testing.T) {
	stub := profileShell(t, "exit 0")
	_, result := ShellEnv(context.Background(), ShellEnvOptions{Shell: stub, LoginShell: true, Variables: []string{"HOME"}})
	assert.Equal(t, ProbeUnknown, result)

	_, result = ShellEnv(context.Background(), ShellEnvOptions{Shell: filepath.Join(t.TempDir(), "no-such-shell"), Variables: []string{"HOME"}})
	assert.Equal(t, ProbeUnknown, result)

	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, result = ShellEnv(ctx, ShellEnvOptions{Shell: shell, Variables: []string{"HOME"}})
	assert.Equal(t, ProbeUnknown, result)
}

func TestShellEnvRefusesANameThatIsNotAnIdentifier(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		ShellEnv(context.Background(), ShellEnvOptions{Shell: "/bin/sh", Variables: []string{"HOME; rm -rf /"}})
	})
}

// A request without variables still reports the native process directory.
func TestShellEnvWithNoVariablesReportsTheProcessDirectory(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	values, result := ShellEnv(context.Background(), ShellEnvOptions{Shell: shell})
	require.Equal(t, ProbeYes, result)
	assert.NotNil(t, values.Values)
	assert.Empty(t, values.Values)

}

// A real tcsh proves that the native child reads set and unset variables.
// tcsh reads its startup file for every shell, so HOME points at an empty directory.
func TestShellEnvReadsTheValuesThroughCsh(t *testing.T) {
	if _, err := exec.LookPath("/bin/tcsh"); err != nil {
		t.Skip("the test needs /bin/tcsh")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LEAPMUX_PROBE_SET", "/a path/with = signs")
	values, result := ShellEnv(context.Background(), ShellEnvOptions{Shell: "/bin/tcsh", Variables: []string{"LEAPMUX_PROBE_SET", "LEAPMUX_PROBE_UNSET"}})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{
		"LEAPMUX_PROBE_SET":   "/a path/with = signs",
		"LEAPMUX_PROBE_UNSET": "",
	}, values.Values)
}

func TestShellEnvPreservesMultilineValues(t *testing.T) {
	shell := profileShell(t, "export LEAPMUX_PROBE_FILE='first\nsecond\r\n'")
	values, result := ShellEnv(t.Context(), ShellEnvOptions{Shell: shell, Variables: []string{"LEAPMUX_PROBE_FILE"}})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, "first\nsecond\r\n", values.Values["LEAPMUX_PROBE_FILE"])
}

func TestShellEnvUsesNativeUnicodeValuesThroughAvailableShells(t *testing.T) {
	for _, name := range []string{"sh", "tcsh", "pwsh", "powershell", "nu"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			home := t.TempDir()
			env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+home, "ZDOTDIR="+home,
				"LEAPMUX_NATIVE_VALUE=overridden", "LEAPMUX_NATIVE_VALUE=日本語=é\nsecond\r\n%$'", "LEAPMUX_NATIVE_EMPTY=")
			snapshot, result := ShellEnv(t.Context(), ShellEnvOptions{Shell: shell, Env: env, WorkingDir: home,
				Variables: []string{"LEAPMUX_NATIVE_VALUE", "LEAPMUX_NATIVE_EMPTY"}})
			require.Equal(t, ProbeYes, result)
			assert.Equal(t, "日本語=é\nsecond\r\n%$'", snapshot.Values["LEAPMUX_NATIVE_VALUE"])
			assert.Empty(t, snapshot.Values["LEAPMUX_NATIVE_EMPTY"])
			assert.True(t, filepath.IsAbs(snapshot.WorkingDir))
		})
	}
}
