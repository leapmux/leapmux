//go:build unix

package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBashInvocationSourcesExactArgumentsAfterProfile(t *testing.T) {
	shell := useBashProfile(t, "function . { printf 'profile dot\\n'; }\nfunction set { printf 'profile set\\n'; }\n")
	parent := filepath.Join(t.TempDir(), "directory with 'quotes'")
	require.NoError(t, os.Mkdir(parent, 0o700))
	t.Setenv("TMPDIR", parent)

	const source = `printf '%s\n' "$SHELLOPTS" 'two\\slashes\'`
	prepared := &PreparedLaunch{}
	entry, err := prepared.writeBashInvocation(source)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	path, found := bashInvocationPath(t, entry)
	require.True(t, found)
	require.FileExists(t, path)
	assert.NotContains(t, entry, source)
	assert.True(t, strings.HasSuffix(renderedWrapScript(t, &exec.Cmd{Args: []string{entry}}), source))

	output, err := exec.CommandContext(testutil.DeadlineContext(t), shell, "-c", entry).CombinedOutput()
	require.NoError(t, err, "%s", output)
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	require.Len(t, lines, 2, "%s", output)
	assert.NotContains(t, strings.Split(lines[0], ":"), "posix")
	assert.Equal(t, `two\\slashes\`, lines[1])

	require.NoError(t, prepared.Close())
	_, err = os.Stat(filepath.Dir(path))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestProfileCommandProtectsBashLaunchSetup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leapmux-control-fixture")
	testutil.WriteExecutable(t, path, "#!/bin/sh\nprintf 'env:%s/%s\\n' \"$LEAPMUX_PRIVATE_PIN\" \"${LEAPMUX_PRIVATE_STRIP+present}\"\nprintf '<%s>\\n' \"$@\"\n")
	shell := useBashProfile(t, "export LEAPMUX_PRIVATE_PIN=before LEAPMUX_PRIVATE_STRIP=before\n"+
		"set() { /usr/bin/printf 'profile-set\\n'; }\n"+
		"export() { /usr/bin/printf 'profile-export\\n'; }\n"+
		"unset() { /usr/bin/printf 'profile-unset\\n'; }\n"+
		"exec() { /usr/bin/printf 'profile-exec\\n'; }\n"+
		"leapmux_control_alias() { set called_by_provider; "+shellquote.POSIX.Quote(path)+" \"$@\"; }\n")
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		label string
		spec  Spec
		want  string
	}{
		{"direct", Spec{Program: path}, ""},
		{"external", ShellCommands("leapmux-control-fixture"), ""},
		{"callable", ShellCommands("leapmux_control_alias"), "profile-set\n"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			cmd, marker, meta := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, Launch: tc.spec,
				BaseArgs:     []string{"app-server", "", "has space"},
				EnvGated:     &EnvGatedArgs{EnvVars: []string{"LEAPMUX_PRIVATE_ABSENT"}, Args: []string{"--optional"}, MetaKey: "optional"},
				StripEnvKeys: []string{"LEAPMUX_PRIVATE_STRIP", "LEAPMUX_PRIVATE_ABSENT"},
				SetEnv:       []string{"LEAPMUX_PRIVATE_PIN=after"},
			})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Equal(t, "\n"+meta+"optional=true\n"+marker+"\n"+tc.want+"env:after/\n<app-server>\n<>\n<has space>\n<--optional>\n", string(output))
		})
	}
}

func TestProfileCommandPosixOptionalArgsWithNounset(t *testing.T) {
	shell := useBashProfile(t, "set -u\nunset LEAPMUX_PRIVATE_OPTION\n")
	for _, spec := range []Spec{{Program: "/usr/bin/printf"}, ShellCommands("leapmux-nounset-peer")} {
		dir := t.TempDir()
		testutil.WriteExecutable(t, filepath.Join(dir, "leapmux-nounset-peer"), "#!/bin/sh\nexec /usr/bin/printf \"$@\"\n")
		t.Setenv("PATH", dir)
		for _, enabled := range []string{"", "profile-override"} {
			t.Setenv("LEAPMUX_PRIVATE_ON", enabled)
			cmd, marker, meta := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, Launch: spec, BaseArgs: []string{"<%s>\n", "base"},
				EnvGated: &EnvGatedArgs{EnvVars: []string{"LEAPMUX_PRIVATE_OPTION", "LEAPMUX_PRIVATE_ON"}, MetaKey: "optional", Args: []string{"optional"}},
			})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			if enabled == "" {
				assert.Equal(t, "\n"+meta+"optional=true\n"+marker+"\n<base>\n<optional>\n", string(output))
			} else {
				assert.Equal(t, "\n"+meta+"optional=false\n"+marker+"\n<base>\n", string(output))
			}
		}
	}
}

func TestProfileCandidateExhaustionUsesTheNativeExit(t *testing.T) {
	shell := useBashProfile(t, "exit() { /usr/bin/printf 'profile-exit:%s\\n' \"$1\"; }\n")
	t.Setenv("PATH", t.TempDir())
	const name = "leapmux-control-missing"
	p, err := Wrap(testutil.DeadlineContext(t), WrapSpec{Shell: shell, Launch: ShellCommands(name)})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, p.Close()) })
	diagnostics := p.NewDiagnostics(1024)
	p.Cmd.Stderr = diagnostics
	output, waitErr := p.Cmd.Output()
	diagnostics.Finish()
	require.Empty(t, output)
	require.ErrorIs(t, diagnostics.ExitError(waitErr, false), ErrNoCandidate)
	var native *exec.ExitError
	require.ErrorAs(t, waitErr, &native)
	assert.Equal(t, 127, native.ExitCode())
}

func TestProfileCandidateUsesNativeRelativePathOperands(t *testing.T) {
	for _, tc := range []struct {
		label, startup, relative string
	}{
		{"unset path", "unset PATH\n", "."},
		{"leading dash", "PATH=-private-bin\n", "-private-bin"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			shell := useBashProfile(t, tc.startup)
			dir := t.TempDir()
			relative := filepath.Join(dir, tc.relative)
			require.NoError(t, os.MkdirAll(relative, 0o700))
			const name = "leapmux-relative-provider"
			testutil.WriteExecutable(t, filepath.Join(relative, name), "#!/bin/sh\nprintf 'relative provider\\n'\n")
			cmd, marker, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{Shell: shell, WorkingDir: dir, Launch: ShellCommands(name)})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Equal(t, "\n"+marker+"\nrelative provider\n", string(output))
			if tc.relative == "." {
				assert.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, "id"), "the probe must honor an explicitly absent PATH")
			}
		})
	}
}

func TestProfileCandidateProbeAcceptsACompletedFrameBeforeExitOutput(t *testing.T) {
	for _, ending := range []string{"", "\\n"} {
		t.Run(strings.TrimPrefix(ending, "\\")+"suffix", func(t *testing.T) {
			shell := useBashProfile(t, "PATH=\nleapmux_probe_callable() { return 0; }\ntrap '/usr/bin/printf \"profile exit"+ending+"\"' EXIT\n")
			assert.Equal(t, map[string]ProbeResult{"leapmux_probe_callable": ProbeYes, "leapmux_probe_absent": ProbeNo},
				checkBinaries(t.Context(), shell, false, []string{"leapmux_probe_callable", "leapmux_probe_absent"}))
		})
	}
}

func TestProgramPathAcceptsAnInterpreterNameWithPunctuation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "node+sqlite")
	testutil.WriteExecutable(t, file, "#!/bin/sh\nexit 0\n")
	shell := useBashProfile(t, "export PATH="+shellquote.POSIX.Quote(dir)+"\n")
	result, status := ProgramPath(t.Context(), shell, false, "node+sqlite")
	require.Equal(t, ProbeYes, status)
	assert.Equal(t, file, result)
}

func TestProgramPathTreatsCshInterpreterNamesAsLiteralData(t *testing.T) {
	for _, name := range []string{"csh", "tcsh"} {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()
			const program = "node+sqlite 'private'"
			file := filepath.Join(dir, program)
			testutil.WriteExecutable(t, file, "#!/bin/sh\nexit 0\n")
			t.Setenv("PATH", dir)
			result, status := ProgramPath(t.Context(), shell, false, program)
			require.Equal(t, ProbeYes, status)
			assert.Equal(t, file, result)
			result, status = ProgramPath(t.Context(), shell, false, program+" missing")
			assert.Equal(t, ProbeNo, status)
			assert.Empty(t, result)
		})
	}
}

func TestProfileCommandZshUsesActiveAliasesAndHashes(t *testing.T) {
	shell := requireProfileShell(t, "zsh", "zsh")
	for _, scenario := range []struct {
		name, profile, want string
	}{
		{"explicit hash", "hash leapmux-zsh-first=hashed-provider\n", "hashed"},
		{"disabled alias", "alias leapmux-zsh-first=missing-alias-target\nunsetopt aliases\n", "fallback"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ZDOTDIR", dir)
			t.Setenv("PATH", dir)
			testutil.WriteExecutable(t, filepath.Join(dir, "hashed-provider"), "#!/bin/sh\nprintf 'hashed\\n'\n")
			testutil.WriteExecutable(t, filepath.Join(dir, "leapmux-zsh-second"), "#!/bin/sh\nprintf 'fallback\\n'\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".zshenv"), []byte(scenario.profile), 0o600))
			cmd, marker, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, WorkingDir: dir, Launch: ShellCommands("leapmux-zsh-first", "leapmux-zsh-second"),
			})
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Equal(t, "\n"+marker+"\n"+scenario.want+"\n", string(output))
			if scenario.want == "fallback" {
				assert.Equal(t, ProbeNo, CheckBinary(t.Context(), shell, false, "leapmux-zsh-first"))
			}
		})
	}
}

func TestProfileCandidateNushellIgnoresShadowedCommandLookup(t *testing.T) {
	shell := requireProfileShell(t, "nu", "nu")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PATH", dir)
	config := filepath.Join(dir, "nushell", "config.nu")
	require.NoError(t, os.MkdirAll(filepath.Dir(config), 0o700))
	require.NoError(t, os.WriteFile(config, []byte("def which [] { [] }\n"), 0o600))
	const candidate = "leapmux-nu-native-peer"
	path := filepath.Join(dir, candidate)
	testutil.WriteExecutable(t, path, "#!/bin/sh\nprintf 'native peer\\n'\n")
	require.Equal(t, ProbeYes, CheckBinary(t.Context(), shell, true, candidate))
	result, status := ProgramPath(t.Context(), shell, true, candidate)
	require.Equal(t, ProbeYes, status)
	canonical, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	assert.Equal(t, canonical, result)
	cmd, marker, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
		Shell: shell, LoginShell: true, WorkingDir: dir, Launch: ShellCommands(candidate),
	})
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	assert.Equal(t, "\n"+marker+"\nnative peer\n", string(output))
}

func TestProfileCommandUsesTheSelectedZshDispatch(t *testing.T) {
	shell := requireProfileShell(t, "zsh", "zsh")
	dir := t.TempDir()
	t.Setenv("ZDOTDIR", dir)
	const external = "leapmux-zsh-launch-peer"
	path := filepath.Join(dir, external)
	testutil.WriteExecutable(t, path, "#!/bin/sh\nprintf 'native <%s>\\n' \"$@\"\nexit 37\n")
	profile := "function builtin { /usr/bin/printf 'profile-builtin\\n'; }\n" +
		"function exec { /usr/bin/printf 'profile-exec\\n'; }\n" +
		"function eval { /usr/bin/printf 'profile-eval\\n'; }\n" +
		"function " + shellquote.POSIX.Quote(path) + " { /usr/bin/printf 'profile-path\\n'; }\n" +
		"leapmux_zsh_alias_body() { eval probe; /usr/bin/printf 'alias <%s>\\n' \"$@\"; return 37; }\n" +
		"alias leapmux-zsh-alias=leapmux_zsh_alias_body\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".zshenv"), []byte(profile), 0o600))
	t.Setenv("PATH", dir)
	for _, tc := range []struct {
		label, want string
		spec        Spec
	}{
		{"direct", "native <>\nnative <one two>\n", Spec{Program: path}},
		{"external", "native <>\nnative <one two>\n", ShellCommands(external)},
		{"alias", "profile-eval\nalias <>\nalias <one two>\n", ShellCommands("leapmux-zsh-alias")},
	} {
		t.Run(tc.label, func(t *testing.T) {
			cmd, marker, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, Launch: tc.spec, BaseArgs: []string{"", "one two"},
			})
			output, err := cmd.CombinedOutput()
			var status *exec.ExitError
			require.ErrorAs(t, err, &status, "%s", output)
			assert.Equal(t, 37, status.ExitCode(), "%s", output)
			assert.Equal(t, "\n"+marker+"\n"+tc.want, string(output))
		})
	}
}

func TestProfileCandidateNushellPassesRequiredPositionAndRest(t *testing.T) {
	shell := requireProfileShell(t, "nu", "nu")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv(profilePeerEnv, "1")
	file := filepath.Join(dir, "nushell", "config.nu")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	peer := "^" + nuQuote(profileCommandExecutable(t)) + " '-test.run=^TestProfileCommandPeer$' '--'"
	profile := "def leapmux-nu-rest [verb: string, ...tail: string] { " + peer + " $verb ...$tail }\n" +
		"def leapmux-nu-fixed [verb: string] { " + peer + " $verb }\n" +
		"$env.LEAPMUX_PRIVATE_HOLD = 'set by the profile'\n"
	require.NoError(t, os.WriteFile(file, []byte(profile), 0o600))
	for _, tc := range []struct {
		name            string
		args            []string
		optionalEnvName string
		want            []string
	}{
		{"leapmux-nu-rest", []string{"app-server", "", "one two"}, "LEAPMUX_PRIVATE_ALLOW", []string{"app-server", "", "one two", "--extra"}},
		{"leapmux-nu-fixed", []string{"app-server"}, "LEAPMUX_PRIVATE_HOLD", []string{"app-server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, marker, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, LoginShell: true, Launch: ShellCommands(tc.name),
				StripEnvKeys: []string{"LEAPMUX_PRIVATE_ALLOW"},
				BaseArgs:     tc.args,
				EnvGated:     &EnvGatedArgs{EnvVars: []string{tc.optionalEnvName}, MetaKey: "optional", Args: []string{"--extra"}},
			})
			assertProfileCommandExchange(t, cmd, marker, tc.want)
		})
	}
}
