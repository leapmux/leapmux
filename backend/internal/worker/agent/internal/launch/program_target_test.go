package launch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/envprobe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testProgramTargetVariable = "LEAPMUX_NATIVE_PINNED_PATH"

func TestProgramTargetReachesTheHelperInTheNativeProfileScope(t *testing.T) {
	executable := nativeFixtureExecutable(t)
	for _, name := range []string{"sh", "bash", "zsh", "fish", "tcsh", "pwsh", "powershell", "nu"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			directory := t.TempDir()
			marker := filepath.Join(directory, "helper executions")
			cmd, delimiter, prefix := Wrap(nativeFixtureContext(t), WrapSpec{Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable},
				BaseArgs: []string{launchFixtureArgument, "echo", "0", marker, "literal argument"}, IndependentProcess: true,
				ProgramTarget: &ProgramTarget{Program: executable, Variable: testProgramTargetVariable},
				StripEnvKeys:  []string{testProgramTargetVariable}, EnvMetadata: &EnvMetadata{Program: executable, Variables: []string{testProgramTargetVariable}},
			})
			cmd.Env = append(cmd.Environ(), "HOME="+directory, "USERPROFILE="+directory, "ZDOTDIR="+directory, "XDG_CONFIG_HOME="+directory, testProgramTargetVariable+"=stale inherited target")
			cmd.Stdin = strings.NewReader("native helper input")
			output, err := cmd.Output()
			require.NoError(t, err, "%q", output)
			before, _, found := strings.Cut(string(output), delimiter)
			require.True(t, found)
			var snapshot envprobe.Snapshot
			for _, line := range strings.Split(before, "\n") {
				if encoded, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), prefix+envprobe.MetadataKey+"="); ok {
					snapshot, err = envprobe.DecodeMetadata(encoded)
					require.NoError(t, err)
				}
			}
			assert.Equal(t, executable, snapshot.Values[testProgramTargetVariable])
			program, found := nativeFixtureOutput(string(output), delimiter)
			require.True(t, found)
			var actual struct {
				Pinned    string   `json:"pinned"`
				Input     string   `json:"input"`
				CWD       string   `json:"cwd"`
				Arguments []string `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal([]byte(program), &actual))
			assert.Equal(t, executable, actual.Pinned)
			assert.Equal(t, "native helper input", actual.Input, "the metadata probe must not consume helper input")
			assert.Equal(t, snapshot.WorkingDir, actual.CWD)
			assert.Equal(t, []string{"literal argument"}, actual.Arguments)
			executions, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.Equal(t, "executed", string(executions))
		})
	}
}

func TestProgramTargetRejectsInvalidSpecificationsBeforeConstructingShellCode(t *testing.T) {
	t.Parallel()
	for _, target := range []ProgramTarget{
		{Program: "native", Variable: "bad;exit"}, {Program: "native", Variable: ""},
		{Variable: "TARGET"}, {Program: "bad\x00path", Variable: "TARGET"}, {Program: string([]byte{255}), Variable: "TARGET"},
	} {
		assert.Panics(t, func() {
			Wrap(context.Background(), WrapSpec{Shell: "/bin/sh", Launch: Spec{Program: "helper"}, ProgramTarget: &target})
		})
	}
}

func TestPowerShellProgramTargetResolvesExecutableAliasesBeforeTheHelper(t *testing.T) {
	executable := nativeFixtureExecutable(t)
	for _, shellName := range []string{"pwsh", "powershell"} {
		t.Run(shellName, func(t *testing.T) {
			shell, err := exec.LookPath(shellName)
			if err != nil {
				t.Skipf("%s is not installed", shellName)
			}
			for _, kind := range []string{"alias", "function", "script", "missing"} {
				t.Run(kind, func(t *testing.T) {
					directory := t.TempDir()
					marker := filepath.Join(directory, "helper executions")
					target := "Invoke-LeapMuxNativeTarget"
					prelude := ""
					switch kind {
					case "alias":
						prelude = "Set-Alias -Name " + target + " -Value " + pwshQuote(executable) + "; "
					case "function":
						prelude = "function " + target + " { & " + pwshQuote(executable) + " @args }; "
					case "script":
						target = filepath.Join(directory, "native target.ps1")
						require.NoError(t, os.WriteFile(target, []byte("& "+pwshQuote(executable)+" @args"), 0o600))
					}
					cmd, delimiter, _ := Wrap(nativeFixtureContext(t), WrapSpec{Shell: shell, WorkingDir: directory, Launch: Spec{Program: executable}, BaseArgs: []string{launchFixtureArgument, "echo", "0", marker}, ProgramTarget: &ProgramTarget{Program: target, Variable: testProgramTargetVariable}})
					cmd.Args[len(cmd.Args)-1] = prelude + cmd.Args[len(cmd.Args)-1]
					cmd.Env = append(cmd.Environ(), "HOME="+directory, "USERPROFILE="+directory, "XDG_CONFIG_HOME="+directory)
					output, runErr := cmd.Output()
					if kind != "alias" {
						require.Error(t, runErr)
						assert.NotContains(t, string(output), delimiter)
						_, err := os.Stat(marker)
						assert.ErrorIs(t, err, os.ErrNotExist)
						return
					}
					require.NoError(t, runErr)
					program, found := nativeFixtureOutput(string(output), delimiter)
					require.True(t, found)
					var actual struct {
						Pinned string `json:"pinned"`
					}
					require.NoError(t, json.Unmarshal([]byte(program), &actual))
					assert.Equal(t, executable, actual.Pinned)
				})
			}
		})
	}
}
