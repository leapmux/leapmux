package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// handoffCall is what one call of controlHandoff.exec received.
type handoffCall struct {
	target string
	argv   []string
	env    []string
}

// handoffHarness is a hand-off whose exec records each call, with the
// executables that a test points it at.
type handoffHarness struct {
	// self is the running executable, as os.Executable reports it.
	self string
	// target is the Worker's CLI. It is another file with the same bytes, as
	// is the copy that a Worker makes when it can make no link. Each hand-off
	// in these tests therefore shows that file identity decides, not content.
	target string
	calls  []handoffCall
	stderr bytes.Buffer
	// execCode and execErr are what exec returns.
	execCode int
	execErr  error
}

func newHandoffHarness(t *testing.T) *handoffHarness {
	t.Helper()
	dir := t.TempDir()
	h := &handoffHarness{
		self:     filepath.Join(dir, "installed", "leapmux"),
		target:   filepath.Join(dir, "cli-1", "leapmux"),
		execCode: 7,
	}
	for _, path := range []string{h.self, h.target} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.WriteExecutable(t, path, "#!/bin/sh\nexit 0\n")
	}
	return h
}

// tabEnv is the environment of a process inside a tab whose Worker linked
// cli. Each entry of overrides replaces the variable that it sets, as in the
// deduplicated environment that os.Environ returns.
func tabEnv(cli string, overrides ...string) []string {
	return envutil.PinEnv([]string{
		"HOME=/home/u",
		controlenv.SocketVariable + "=unix:/run/leapmux/control.sock",
		controlenv.TokenVariable + "=token-1",
		controlenv.CLIVariable + "=" + cli,
	}, overrides...)
}

// run runs the hand-off of args in environ, and returns its answer.
func (h *handoffHarness) run(environ []string, args ...string) (int, bool) {
	return h.runAs(func() (string, error) { return h.self, nil }, environ, args...)
}

func (h *handoffHarness) runAs(executable func() (string, error), environ []string, args ...string) (int, bool) {
	handoff := controlHandoff{
		environ:    environ,
		executable: executable,
		exec: func(target string, argv, env []string) (int, error) {
			h.calls = append(h.calls, handoffCall{target: target, argv: argv, env: env})
			return h.execCode, h.execErr
		},
		stderr: &h.stderr,
	}
	return handoff.run(args)
}

func TestControlHandoffHandsAControlCommandToTheWorkerCLI(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"tab", "list"},
		{"tab"},
		{"tab", "--help"},
		{"agent", "send", "87a19d5f", "--", "-v"},
		{"agent", "a-command-of-a-newer-release", "--a-flag-of-a-newer-release"},
		{"ag", "ls"},
		{"--help", "tab", "list"},
		{"--hub", "https://hub.example", "tab", "list"},
		{"--format", "json", "tab", "list"},
		{"-qw", "tab", "list"},
		{"-q=true", "tab", "list"},
		{"help", "tab", "list"},
		{"help", "ag"},
		{"help", "w"},
		{"w"},
		{"t"},
		{"-"},
		{"__complete", "tab", ""},
		{"__complete", "help", "tab", ""},
		{"__complete", "--hub", "https://hub.example", "tab", ""},
		{"__completeNoDesc", "worker", ""},
		{"new-control-cmd"},
		{"new-control-cmd", "list"},
		{"help", "new-control-cmd"},
		{"--new-global-flag", "tab", "list"},
		{"--new-global-flag=1", "tab", "list"},
		{"-x", "tab", "list"},
		{"-qx", "tab", "list"},
		{"-=foo", "tab", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			environ := tabEnv(h.target)

			code, handled := h.run(environ, args...)

			assert.True(t, handled)
			assert.Equal(t, 7, code, "the exit code is the Worker CLI's")
			require.Len(t, h.calls, 1)
			assert.Equal(t, h.target, h.calls[0].target)
			assert.Equal(t, append([]string{h.target}, args...), h.calls[0].argv,
				"the Worker's CLI receives the arguments as typed, and parses them itself")
			assert.Equal(t, append(environ, controlHandoffMarker+"=1"), h.calls[0].env,
				"the Worker's CLI inherits the environment, with the marker that stops a second hand-off")
			assert.Empty(t, h.stderr.String())
		})
	}
}

// Every local command describes or acts on the binary that the user ran. The
// target does not exist here, and no warning proves that the hand-off never
// looked at it.
func TestControlHandoffLeavesEveryOtherCommandHere(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"daemon", "worker", "run"},
		{"daemon", "solo", "start"},
		{"up"},
		{"down"},
		{"recover", "db", "path"},
		{"version"},
		{"--version"},
		{"-v"},
		{"commands", "--json"},
		{"completion", "zsh"},
		{"help"},
		{"help", "daemon"},
		{"help", "da"},
		{"help", "c"},
		{"help", "help"},
		{"help", "help", "tab"},
		{"help", "-h"},
		{"--help"},
		{"-h"},
		{"--hub", "https://hub.example", "daemon", "solo", "status"},
		{"--hub=https://hub.example", "recover", "db", "path"},
		{"--format", "json", "version"},
		{"-qw", "commands"},
		{"--hub"},
		{"--format"},
		{""},
		{"__complete"},
		{"__complete", "tab"},
		{"__completeNoDesc", "tab"},
		{"__complete", "daemon", ""},
		{"__complete", "help", "daemon", ""},
		{"c", "tab", "list"},
		{"--", "tab"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.run(tabEnv(filepath.Join(t.TempDir(), "missing", "leapmux")), args...)

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

func TestControlHandoffNeedsTheContextOfATab(t *testing.T) {
	t.Parallel()
	for name, environ := range map[string]func(cli string) []string{
		"without the socket": func(cli string) []string {
			return []string{controlenv.TokenVariable + "=token-1", controlenv.CLIVariable + "=" + cli}
		},
		"with an empty socket": func(cli string) []string { return tabEnv(cli, controlenv.SocketVariable+"=") },
		"without the token": func(cli string) []string {
			return []string{controlenv.SocketVariable + "=unix:/s", controlenv.CLIVariable + "=" + cli}
		},
		"with an empty token": func(cli string) []string { return tabEnv(cli, controlenv.TokenVariable+"=") },
		// A Worker without a link, such as the desktop app's AppImage, sets
		// no LEAPMUX_CONTROL_CLI.
		"without the Worker's CLI": func(string) []string {
			return []string{controlenv.SocketVariable + "=unix:/s", controlenv.TokenVariable + "=token-1"}
		},
		"with an empty Worker's CLI": func(cli string) []string { return tabEnv(cli, controlenv.CLIVariable+"=") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.run(environ(h.target), "tab", "list")

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

// A process that a hand-off started never hands the command off again, so a
// target that leads back to this `leapmux` cannot start one process after
// another.
func TestControlHandoffHonorsTheMarker(t *testing.T) {
	t.Parallel()
	h := newHandoffHarness(t)

	_, handled := h.run(tabEnv(h.target, controlHandoffMarker+"=1"), "tab", "list")

	assert.False(t, handled)
	assert.Empty(t, h.calls)
	assert.Empty(t, h.stderr.String())

	t.Run("an empty marker is no marker", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)

		_, handled := h.run(tabEnv(h.target, controlHandoffMarker+"="), "tab", "list")

		assert.True(t, handled)
		require.Len(t, h.calls, 1)
		// Go's os.Getenv and the C library read the first entry of a name, so
		// the target must find no entry but the marker that the hand-off set.
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(h.calls[0].env, controlHandoffMarker))
	})
}

// The comparison reads file identity: the path itself, a hard link and a
// symbolic link all name the running executable, and running it again would
// only cost a process.
func TestControlHandoffKeepsACommandForTheRunningExecutable(t *testing.T) {
	t.Parallel()
	for name, link := range map[string]func(self, link string) error{
		"the path itself": nil,
		"a hard link":     os.Link,
		"a symbolic link": os.Symlink,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			target := h.self
			if link != nil {
				target = filepath.Join(t.TempDir(), "leapmux")
				if err := link(h.self, target); err != nil {
					// Windows refuses a symbolic link without a privilege.
					t.Skipf("cannot create %s: %v", name, err)
				}
			}

			_, handled := h.run(tabEnv(target), "tab", "list")

			assert.False(t, handled)
			assert.Empty(t, h.calls)
			assert.Empty(t, h.stderr.String())
		})
	}
}

func TestControlHandoffWarnsAndRunsOnWhenTheTargetCannotRun(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		target func(t *testing.T) string
		reason string
	}{
		"a missing file": {
			target: func(t *testing.T) string { return filepath.Join(t.TempDir(), "leapmux") },
			reason: "does not exist",
		},
		"a directory": {
			target: func(t *testing.T) string { return t.TempDir() },
			reason: "is not a file",
		},
		"a relative path": {
			target: func(*testing.T) string { return filepath.Join("cli-1", "leapmux") },
			reason: "is not an absolute path",
		},
	}
	if runtime.GOOS != "windows" {
		cases["a file without the execute permission"] = struct {
			target func(t *testing.T) string
			reason string
		}{
			target: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "leapmux")
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600))
				return path
			},
			reason: "is not executable",
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)
			target := tc.target(t)

			_, handled := h.run(tabEnv(target), "tab", "list")

			assert.False(t, handled, "this leapmux runs the command")
			assert.Empty(t, h.calls)
			assert.Contains(t, h.stderr.String(), "warning: ")
			assert.Contains(t, h.stderr.String(), target)
			assert.Contains(t, h.stderr.String(), tc.reason)
			assert.Contains(t, h.stderr.String(), "This leapmux runs the command, and its version can differ from the Worker's.")
		})
	}
}

func TestControlHandoffWarnsAndRunsOnWhenTheTargetDoesNotStart(t *testing.T) {
	t.Parallel()
	h := newHandoffHarness(t)
	h.execErr = errors.New("exec format error")

	_, handled := h.run(tabEnv(h.target), "tab", "list")

	assert.False(t, handled)
	assert.Len(t, h.calls, 1)
	assert.Equal(t, "warning: the Worker's CLI "+h.target+" did not start: exec format error. "+
		"This leapmux runs the command, and its version can differ from the Worker's.\n", h.stderr.String())
}

// Without the path of the running executable, the hand-off cannot tell the
// two apart. It hands the command off, and the marker stops a second one.
func TestControlHandoffHandsOffWhenItCannotReadItself(t *testing.T) {
	t.Parallel()
	for name, executable := range map[string]func() (string, error){
		"an error":       func() (string, error) { return "", errors.New("not supported") },
		"a missing path": func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHandoffHarness(t)

			_, handled := h.runAs(executable, tabEnv(h.target), "tab", "list")

			assert.True(t, handled)
			assert.Len(t, h.calls, 1)
		})
	}
}

// The hand-off reads a variable as os.Getenv and the C library do: the first
// entry wins, and only Windows ignores the case of a name.
func TestControlHandoffReadsTheEnvironmentAsOSGetenvDoes(t *testing.T) {
	t.Parallel()

	t.Run("the first entry wins", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)
		environ := append(tabEnv(h.target), controlenv.CLIVariable+"="+filepath.Join(t.TempDir(), "missing"))

		_, handled := h.run(environ, "tab", "list")

		assert.True(t, handled)
		require.Len(t, h.calls, 1)
		assert.Equal(t, h.target, h.calls[0].target)
		assert.Empty(t, h.stderr.String(), "the later entry is never read")
	})

	t.Run("the case of a name", func(t *testing.T) {
		t.Parallel()
		h := newHandoffHarness(t)
		environ := []string{
			strings.ToLower(controlenv.SocketVariable) + "=unix:/s",
			strings.ToLower(controlenv.TokenVariable) + "=token-1",
			strings.ToLower(controlenv.CLIVariable) + "=" + h.target,
		}

		_, handled := h.run(environ, "tab", "list")

		assert.Equal(t, runtime.GOOS == "windows", handled)
	})
}

// Every local-only command exists in the root command tree and is marked via
// cli.MarkLocalOnly, while control commands are not marked local-only.
func TestLocalOnlyCommandsExistInRootTree(t *testing.T) {
	t.Parallel()
	root := newRootCommand(testCLIRunners(t))
	wantLocal := map[string]bool{
		"daemon":     true,
		"up":         true,
		"down":       true,
		"recover":    true,
		"version":    true,
		"commands":   true,
		"completion": true,
		"help":       true,
	}
	seenLocal := map[string]bool{}
	for _, child := range root.Commands() {
		assert.Equal(t, wantLocal[child.Name()], cli.IsLocalOnly(child), "local-only marking for %q", child.Name())
		if cli.IsLocalOnly(child) {
			seenLocal[child.Name()] = true
		}
	}
	assert.Equal(t, wantLocal, seenLocal)
}

// The marker served the process that a hand-off started. A process that it
// starts decides for itself.
//
// Not parallel: the test sets a variable of this process.
func TestHandOffControlCommandClearsTheMarker(t *testing.T) {
	t.Setenv(controlHandoffMarker, "1")

	_, handled := handOffControlCommand([]string{"version"}, nil)

	assert.False(t, handled)
	_, present := os.LookupEnv(controlHandoffMarker)
	assert.False(t, present)
}

func TestControlHandoffSkipsTreeBuildWhenTargetIsSelf(t *testing.T) {
	t.Parallel()
	h := newHandoffHarness(t)
	treeBuilds := 0
	handoff := controlHandoff{
		environ:    tabEnv(h.self),
		executable: func() (string, error) { return h.self, nil },
		exec: func(target string, argv, env []string) (int, error) {
			t.Fatal("self target must not exec")
			return 0, nil
		},
		root: func() *cobra.Command {
			treeBuilds++
			return newRootCommand(defaultCLIRunners())
		},
		stderr: &h.stderr,
	}

	_, handled := handoff.run([]string{"tab", "list"})
	assert.False(t, handled)
	assert.Zero(t, treeBuilds, "self target must not build the command tree")
	assert.Empty(t, h.stderr.String())

	t.Run("missing tab context or marker also skips tree build", func(t *testing.T) {
		t.Parallel()
		for name, env := range map[string][]string{
			"no tab env": nil,
			"marker set": tabEnv(h.target, controlHandoffMarker+"=1"),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				builds := 0
				ho := controlHandoff{
					environ:    env,
					executable: func() (string, error) { return h.self, nil },
					exec: func(string, []string, []string) (int, error) {
						t.Fatal("must not exec")
						return 0, nil
					},
					root: func() *cobra.Command {
						builds++
						return newRootCommand(defaultCLIRunners())
					},
					stderr: &bytes.Buffer{},
				}
				_, ok := ho.run([]string{"tab", "list"})
				assert.False(t, ok)
				assert.Zero(t, builds)
			})
		}
	})
}
