package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A shell that cannot START proves nothing about the binary. Caching that
// false froze a broken environment as "not installed" for the worker's
// lifetime, and the availability scan still reported complete=true, so
// the client never retried and the new-agent button stayed empty until a
// restart.
func TestProbeBinaryInconclusiveWhenTheShellCannotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-shell")

	assert.Equal(t, ProbeUnknown, probeBinary(context.Background(), missing, false, "claude"),
		"a shell that never ran establishes nothing")

	// And nothing is cached, so a later call with a working shell is free
	// to answer for itself. The inconclusiveness reaches the caller too:
	// The availability scan needs it to tell "the shell said no" apart
	// from "the shell never ran".
	assert.Equal(t, ProbeUnknown, CheckBinary(context.Background(), missing, false, "claude"),
		"checkBinaryAvailable must forward the probe's own verdict")
	_, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{missing, false, "claude"})
	assert.False(t, cached, "an inconclusive probe must not be cached")
}

// A login profile that exits before the inner command used to look like
// "binary absent": both are ExitError. The reached marker is what
// distinguishes them, and the miss must not be cached.
func TestProbeBinaryInconclusiveWhenTheShellExitsBeforeTheProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang stub cannot exec on Windows")
	}
	stub := filepath.Join(t.TempDir(), "shell")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	assert.Equal(t, ProbeUnknown, probeBinary(context.Background(), stub, true, "claude"),
		"a shell that exits before the inner command establishes nothing")

	_, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{stub, true, "claude"})
	assert.False(t, cached, "an inconclusive probe must not be cached")
}

// Exit 0 without the reached marker used to look like "binary present".
// A login profile that `exit 0`s before the inner command, or a stub
// that ignores argv, must stay inconclusive.
func TestProbeBinaryInconclusiveWhenTheShellExitsZeroWithoutAMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang stub cannot exec on Windows")
	}
	stub := filepath.Join(t.TempDir(), "shell")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	assert.Equal(t, ProbeUnknown, probeBinary(context.Background(), stub, true, "claude"),
		"exit 0 without the reached marker establishes nothing")

	_, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{stub, true, "claude"})
	assert.False(t, cached, "an inconclusive probe must not be cached")
}

// A shell that runs and reports the binary absent IS an answer, and is
// cached — that is the case the cache exists for.
func TestProbeBinaryConclusiveWhenTheShellAnswers(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	const absent = "leapmux-definitely-not-installed"

	assert.Equal(t, ProbeNo, probeBinary(context.Background(), shell, false, absent),
		"the shell ran and answered")

	assert.Equal(t, ProbeNo, CheckBinary(context.Background(), shell, false, absent))
	v, cached := binaryAvailabilityCache.Load(binaryAvailabilityKey{shell, false, absent})
	require.True(t, cached, "a settled probe must be cached")
	assert.Equal(t, ProbeNo, v)
}

func TestProbeBinaryConclusiveWhenTheBinaryIsPresent(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}

	assert.Equal(t, ProbeYes, probeBinary(context.Background(), shell, false, "echo"),
		"echo is a POSIX builtin, so command -v must find it")
}

// A probe killed by an expired context reports an ExitError ("signal:
// killed"), not the context error — so the status alone would look
// conclusive.
func TestProbeBinaryInconclusiveUnderAnExpiredContext(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Equal(t, ProbeUnknown, probeBinary(ctx, shell, false, "leapmux-cancelled-probe"),
		"a probe under a dead context establishes nothing")
}

// A present marker with no path after it is INCONCLUSIVE, not absent: the shell said the
// program resolves, so reporting absence would contradict the only evidence there is.
func TestParseProgramPathProbe(t *testing.T) {
	t.Parallel()

	// The paths are built for the running OS, because the parser accepts only a
	// path this OS can execute -- see testutil.NativeAbsPath.
	resolved := testutil.NativeAbsPath("/usr/local/bin/node")
	system := testutil.NativeAbsPath("/usr/bin/node")

	cases := map[string]struct {
		out  string
		path string
		want ProbeResult
	}{
		"a resolved absolute path": {
			probeReachedPresent + "\n" + resolved + "\n", resolved, ProbeYes,
		},
		"blank lines before the path are skipped": {
			probeReachedPresent + "\n\n  " + system + "  \n", system, ProbeYes,
		},
		"profile noise before the marker is ignored": {
			"welcome to your shell\n" + probeReachedPresent + "\n" + system + "\n", system, ProbeYes,
		},
		"a settled absence": {
			probeReachedAbsent + "\n", "", ProbeNo,
		},
		"present with no path establishes nothing": {
			probeReachedPresent + "\n", "", ProbeUnknown,
		},
		"a relative path establishes nothing": {
			probeReachedPresent + "\nnode\n", "", ProbeUnknown,
		},
		"a shell builtin name establishes nothing": {
			probeReachedPresent + "\nnode: shell built-in command\n", "", ProbeUnknown,
		},
		"no marker at all establishes nothing": {
			"command not found\n", "", ProbeUnknown,
		},
		"empty output establishes nothing": {
			"", "", ProbeUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path, res := parseProgramPathProbe(tc.out)
			assert.Equal(t, tc.path, path)
			assert.Equal(t, tc.want, res)
			// A path is only ever returned WITH ProbeYes, which is the pairing the
			// boolean form could contradict.
			assert.Equal(t, res == ProbeYes, path != "")
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
}

// TestLaunchLocatorBinariesCopiesTheNames pins that a locator owns its name list:
// a caller that reuses its slice cannot change what the locator probes.
func TestLaunchLocatorBinariesCopiesTheNames(t *testing.T) {
	t.Parallel()

	names := []string{"first", "second"}
	l := Binaries(names...)
	names[0] = "changed"
	assert.Equal(t, []string{"first", "second"}, l.binaries)
}

// A resolver's three states reach the availability answer unchanged.
func TestLaunchLocatorAvailableForwardsTheResolverAnswer(t *testing.T) {
	t.Parallel()

	for _, res := range []Resolution{Found, Missing, Unknown} {
		l := Custom(func(context.Context, string, bool) (Spec, Resolution) { return Spec{}, res })
		assert.Equal(t, res, l.Available(context.Background(), "/bin/sh", false))
	}
}

// A shell that runs and answers "absent" for every name is a conclusive absence.
func TestLaunchLocatorAvailableIsMissingWhenTheShellAnswers(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	// A PATH with nothing on it makes every probe answer "absent" fast. The names are
	// unique to this test, so no other test's cached answer can stand in for a probe.
	t.Setenv("PATH", t.TempDir())
	l := Binaries("leapmux-locator-absent-a", "leapmux-locator-absent-b")
	assert.Equal(t, Missing, l.Available(context.Background(), shell, false))
}

// A shell that never ran establishes nothing, so the answer is retryable.
func TestLaunchLocatorAvailableIsUnknownWhenTheShellCannotStart(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-shell")
	l := Binaries("leapmux-locator-unreached")
	assert.Equal(t, Unknown, l.Available(context.Background(), missing, false))
}

// Any one name that probes present is enough, whatever the names before it say.
func TestLaunchLocatorAvailableIsFoundWhenAnyNameIsPresent(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	// echo is a POSIX builtin, so `command -v echo` finds it with any PATH.
	l := Binaries("leapmux-locator-absent-c", "echo")
	assert.Equal(t, Found, l.Available(context.Background(), shell, false))
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
	t.Run("binaries fall back to the preferred name when none is present", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-shell")
		got, err := Binaries("preferred", "other").Resolve(context.Background(), missing, false, "Tool")
		require.NoError(t, err, "the shell reports a missing command; the locator does not")
		assert.Equal(t, Spec{Program: "preferred"}, got)
	})
}
