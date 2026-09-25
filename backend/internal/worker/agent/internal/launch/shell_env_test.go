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

	values, result := ShellEnv(context.Background(), shell, false, []string{"LEAPMUX_PROBE_FILE", "LEAPMUX_PROBE_WORKER_ONLY", "LEAPMUX_PROBE_UNSET"})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{
		"LEAPMUX_PROBE_FILE":        "/a path/with = signs",
		"LEAPMUX_PROBE_WORKER_ONLY": "",
		"LEAPMUX_PROBE_UNSET":       "",
	}, values)
}

// A value that holds a quote or a dollar sign reaches the caller as it is: the
// probe expands the variable and never evaluates its value.
func TestShellEnvKeepsSpecialCharacters(t *testing.T) {
	shell := profileShell(t, `export LEAPMUX_PROBE_FILE='it'"'"'s $HOME/%s'`)
	values, result := ShellEnv(context.Background(), shell, false, []string{"LEAPMUX_PROBE_FILE"})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, `it's $HOME/%s`, values["LEAPMUX_PROBE_FILE"])
}

// A shell that exits before the probe establishes nothing, and so does one
// that cannot start.
func TestShellEnvInconclusive(t *testing.T) {
	stub := profileShell(t, "exit 0")
	_, result := ShellEnv(context.Background(), stub, true, []string{"HOME"})
	assert.Equal(t, ProbeUnknown, result)

	_, result = ShellEnv(context.Background(), filepath.Join(t.TempDir(), "no-such-shell"), false, []string{"HOME"})
	assert.Equal(t, ProbeUnknown, result)

	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, result = ShellEnv(ctx, shell, false, []string{"HOME"})
	assert.Equal(t, ProbeUnknown, result)
}

func TestShellEnvRefusesANameThatIsNotAnIdentifier(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { ShellEnv(context.Background(), "/bin/sh", false, []string{"HOME; rm -rf /"}) })
}

func TestParseShellEnvProbe(t *testing.T) {
	t.Parallel()
	values, result := parseShellEnvProbe("profile noise\n"+probeReachedPresent+"\n"+shellEnvPrefix+"A=1\r\n"+shellEnvPrefix+"C=3\nstray\n", []string{"A", "B"})
	assert.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{"A": "1", "B": ""}, values, "a name that was not asked for is dropped")

	_, result = parseShellEnvProbe(shellEnvPrefix+"A=1\n", []string{"A"})
	assert.Equal(t, ProbeUnknown, result, "no marker, no answer")
}

// Each dialect's probe is shell code for that dialect.
func TestShellEnvProbeDialects(t *testing.T) {
	t.Parallel()
	names := []string{"HOME", "XDG_CONFIG_HOME"}
	assert.Equal(t, `printf '%s\n' '`+probeReachedPresent+`' "`+shellEnvPrefix+`HOME=$HOME" "`+shellEnvPrefix+`XDG_CONFIG_HOME=$XDG_CONFIG_HOME"`, posixEnvProbe(names))
	assert.Contains(t, cshEnvProbe(names), "if ( $?XDG_CONFIG_HOME ) then\n")
	assert.Contains(t, nuEnvProbe(names), `($env | get -i HOME | default '')`)
	assert.Contains(t, pwshEnvProbe(names), `+ $env:XDG_CONFIG_HOME)`)
}

// A probe of no name still states whether the shell reached it, so a caller
// learns that the profile ran to the end.
func TestShellEnvWithNoNamesStatesOnlyTheMarker(t *testing.T) {
	t.Parallel()
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	values, result := ShellEnv(context.Background(), shell, false, nil)
	require.Equal(t, ProbeYes, result)
	assert.NotNil(t, values)
	assert.Empty(t, values)

	values, result = parseShellEnvProbe(probeReachedPresent+"\n", nil)
	assert.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{}, values)
}

// csh fails on an unset name, so its probe guards each read, and the guard and
// the read sit on separate lines. A real tcsh proves that the probe runs and
// that an unset name reads as "". tcsh reads its startup file for every shell,
// so HOME points at an empty directory. t.Setenv keeps the test serial.
func TestShellEnvReadsTheValuesThroughCsh(t *testing.T) {
	if _, err := exec.LookPath("/bin/tcsh"); err != nil {
		t.Skip("the test needs /bin/tcsh")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LEAPMUX_PROBE_SET", "/a path/with = signs")
	values, result := ShellEnv(context.Background(), "/bin/tcsh", false, []string{"LEAPMUX_PROBE_SET", "LEAPMUX_PROBE_UNSET"})
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, map[string]string{
		"LEAPMUX_PROBE_SET":   "/a path/with = signs",
		"LEAPMUX_PROBE_UNSET": "",
	}, values)
}
