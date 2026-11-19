package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckBinaryInconclusiveWhenTheShellCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shell")
	assert.Equal(t, ProbeUnknown, CheckBinary(t.Context(), missing, false, "claude"))
}

func TestCheckBinaryInconclusiveWhenTheProfileExitsBeforeTheProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell profile")
	}
	for _, exitCode := range []string{"0", "1"} {
		t.Run(exitCode, func(t *testing.T) {
			stub := filepath.Join(t.TempDir(), "shell")
			testutil.WriteExecutable(t, stub, "#!/bin/sh\nexit "+exitCode+"\n")
			assert.Equal(t, ProbeUnknown, CheckBinary(t.Context(), stub, true, "claude"))
		})
	}
}

func TestCheckBinariesUsesOneShellForPresentAndAbsentNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell profile")
	}
	shellDir := t.TempDir()
	script := filepath.Join(shellDir, "shell")
	log := filepath.Join(shellDir, "launches")
	testutil.WriteExecutable(t, script, "#!/bin/sh\nprintf 'launch\\n' >> "+shellquote.POSIX.Quote(log)+"\nprintf 'profile banner'\nexec /bin/sh \"$@\"\n")

	const absent = "leapmux-definitely-not-installed"
	const present = "leapmux-probe-executable"
	testutil.WriteExecutable(t, filepath.Join(shellDir, present), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", shellDir)
	assert.Equal(t, map[string]ProbeResult{
		present: ProbeYes,
		absent:  ProbeNo,
	}, checkBinaries(t.Context(), script, false, []string{present, absent}))
	launches, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "launch\n", string(launches))
}

func TestCheckBinariesThroughTcsh(t *testing.T) {
	tcsh, err := exec.LookPath("tcsh")
	if err != nil {
		t.Skip("tcsh is unavailable")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	const present = "leapmux-probe-executable"
	const absent = "leapmux-definitely-not-installed"
	testutil.WriteExecutable(t, filepath.Join(dir, present), "#!/bin/sh\nexit 0\n")
	assert.Equal(t, map[string]ProbeResult{
		present: ProbeYes,
		absent:  ProbeNo,
	}, checkBinaries(t.Context(), tcsh, false, []string{present, absent}))
}

func TestCheckBinariesThroughTcshWithAliasedEcho(t *testing.T) {
	tcsh, err := exec.LookPath("tcsh")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("tcsh with a POSIX profile is unavailable")
	}
	home := t.TempDir()
	const installed = "test-provider"
	testutil.WriteExecutable(t, filepath.Join(home, installed), "#!/bin/sh\nexit 0\n")
	profile := "alias echo '/bin/echo -n'\nset path = (" + shellquote.POSIX.Quote(home) + ")\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".tcshrc"), []byte(profile), 0o600))
	t.Setenv("HOME", home)

	const absent = "leapmux-definitely-not-installed"
	assert.Equal(t, map[string]ProbeResult{
		installed: ProbeYes,
		absent:    ProbeNo,
	}, checkBinaries(t.Context(), tcsh, true, []string{installed, absent}))
}

func TestProgramPathAfterProfileOutputWithoutNewline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX profile")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	shell := filepath.Join(dir, "shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf 'profile banner'\nexec /bin/sh \"$@\"\n")
	const candidate = "leapmux-profile-path"
	executable := filepath.Join(dir, candidate)
	testutil.WriteExecutable(t, executable, "#!/bin/sh\nexit 0\n")
	path, result := ProgramPath(t.Context(), shell, false, candidate)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, executable, path)
}

func TestProgramPathThroughFish(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is unavailable")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PATH", dir)
	const candidate = "leapmux-fish-path"
	executable := filepath.Join(dir, candidate)
	testutil.WriteExecutable(t, executable, "#!/bin/sh\nexit 0\n")
	path, result := ProgramPath(t.Context(), fish, false, candidate)
	require.Equal(t, ProbeYes, result)
	assert.Equal(t, executable, path)
}

func TestCheckBinaryInconclusiveUnderAnExpiredContext(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	assert.Equal(t, ProbeUnknown, CheckBinary(ctx, shell, false, "leapmux-cancelled-probe"))
}

func TestParseCommandProbe(t *testing.T) {
	t.Parallel()
	path := testutil.NativeAbsPath("/usr/local/bin/node")
	for _, test := range []struct {
		name   string
		output string
		result ProbeResult
		path   string
	}{
		{"a completed absolute path", "banner\nprobe:start\nprobe:0:yes:" + path + "\nprobe:complete\n", ProbeYes, path},
		{"completed absence", "probe:start\r\nprobe:0:no\r\nprobe:complete\r\n", ProbeNo, ""},
		{"completion without LF", "probe:start\nprobe:0:no\nprobe:complete", ProbeUnknown, ""},
		{"completion without CRLF", "probe:start\r\nprobe:0:no\r\nprobe:complete\r", ProbeUnknown, ""},
		{"missing completion", "probe:start\nprobe:0:no\n", ProbeUnknown, ""},
		{"duplicate result", "probe:start\nprobe:0:no\nprobe:0:no\nprobe:complete\n", ProbeUnknown, ""},
		{"relative path", "probe:start\nprobe:0:yes:node\nprobe:complete\n", ProbeUnknown, ""},
		{"malformed record", "probe:start\nprobe:0:yes\nprobe:complete\n", ProbeUnknown, ""},
		{"no start", "probe:0:no\nprobe:complete\n", ProbeUnknown, ""},
		{"empty output", "", ProbeUnknown, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			results, gotPath := parseCommandProbe(test.output, "probe", []string{"node"}, true)
			assert.Equal(t, test.result, results["node"])
			assert.Equal(t, test.path, gotPath)
		})
	}
}

// TestLaunchLocatorValid pins that a locator states exactly one way to find the
// program: a zero locator, and one with an empty name list, cannot start anything.
func TestLaunchLocatorValid(t *testing.T) {
	t.Parallel()

	resolver := func(context.Context, string, bool) (Spec, Resolution) { return Spec{}, Found }
	assert.True(t, Binaries("claude").Valid())
	assert.True(t, Custom(resolver).Valid())
	assert.False(t, Locator{}.Valid(), "a zero locator states no way to find the program")
	assert.False(t, Binaries().Valid(), "an empty name list states no way to find the program")
	assert.False(t, Custom(nil).Valid(), "a nil resolver states no way to find the program")
	for _, name := range []string{"", "two words", "../provider", "first;second", "-option"} {
		locator := Binaries("valid", name)
		assert.False(t, locator.Valid(), "%q must be rejected", name)
		_, err := locator.Resolve(t.Context(), "unused", true, "Tool")
		assert.ErrorContains(t, err, "no valid provider commands registered")
	}
}

// TestLaunchLocatorBinariesCopiesTheNames pins that a locator owns its name list:
// a caller that reuses its slice cannot change what the locator probes.
func TestLaunchLocatorBinariesCopiesTheNames(t *testing.T) {
	t.Parallel()

	names := []string{"first", "second"}
	l := Binaries(names...)
	names[0] = "changed"
	first, err := l.Resolve(t.Context(), "unused", true, "Tool")
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, first.candidates)
	first.candidates[1] = "changed"
	second, err := l.Resolve(t.Context(), "unused", true, "Tool")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, second.candidates)
}

// A resolver's three states reach the availability answer unchanged.
func TestScanAvailabilityForwardsTheResolverAnswer(t *testing.T) {
	t.Parallel()

	for _, res := range []Resolution{Found, Missing, Unknown} {
		l := Custom(func(context.Context, string, bool) (Spec, Resolution) { return Spec{}, res })
		assert.Equal(t, res, ScanAvailability(t.Context(), "/bin/sh", false, []Locator{l})[0])
	}
}

// A shell that runs and answers "absent" for every name is a conclusive absence.
func TestScanAvailabilityIsMissingWhenTheShellAnswers(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	// A PATH with no executables makes every probe report absence.
	t.Setenv("PATH", t.TempDir())
	l := Binaries("leapmux-locator-absent-a", "leapmux-locator-absent-b")
	assert.Equal(t, Missing, ScanAvailability(t.Context(), shell, false, []Locator{l})[0])
}

// A shell that never ran establishes nothing, so the answer is retryable.
func TestScanAvailabilityIsUnknownWhenTheShellCannotStart(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-shell")
	l := Binaries("leapmux-locator-unreached")
	assert.Equal(t, Unknown, ScanAvailability(t.Context(), missing, false, []Locator{l})[0])
}

// Any one name that probes present is enough, whatever the names before it say.
func TestScanAvailabilityIsFoundWhenAnyNameIsPresent(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	const present = "leapmux-locator-present"
	testutil.WriteExecutable(t, filepath.Join(dir, present), "#!/bin/sh\nexit 0\n")
	l := Binaries("leapmux-locator-absent-c", present)
	assert.Equal(t, Found, ScanAvailability(t.Context(), shell, false, []Locator{l})[0])
}

func TestLaunchLocatorResolve(t *testing.T) {
	t.Parallel()

	t.Run("a found resolver answer is used verbatim", func(t *testing.T) {
		want := Spec{Program: "/opt/node", PrefixArgs: []string{"/opt/tool.cjs"}, Env: []string{"K=V"}}
		l := Custom(func(context.Context, string, bool) (Spec, Resolution) { return want, Found })
		got, err := l.Resolve(context.Background(), "/bin/sh", false, "Tool")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
	for _, tc := range []struct {
		name string
		res  Resolution
		want string
	}{
		{"missing", Missing, "Tool is not installed on this machine"},
		{"unknown", Unknown, "could not determine how to launch Tool"},
	} {
		t.Run(tc.name+" is an error naming the provider", func(t *testing.T) {
			l := Custom(func(context.Context, string, bool) (Spec, Resolution) { return Spec{}, tc.res })
			_, err := l.Resolve(context.Background(), "/bin/sh", false, "Tool")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	t.Run("a locator with no way to find the program is an error", func(t *testing.T) {
		_, err := Locator{}.Resolve(context.Background(), "/bin/sh", false, "Tool")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Tool")
	})
	t.Run("binaries preserve ordered names when the shell is unavailable", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-shell")
		got, err := Binaries("preferred", "other").Resolve(context.Background(), missing, false, "Tool")
		require.NoError(t, err, "the shell reports a missing command; the locator does not")
		assert.Equal(t, ShellCommands("preferred", "other"), got)
	})
}
