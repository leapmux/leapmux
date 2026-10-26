//go:build unix

package launch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

const wrappedArgumentsHelperEnv = "LEAPMUX_WRAPPED_ARGUMENTS_HELPER"

const argumentsReportScript = "ConvertTo-Json -Compress -InputObject @{ arguments = @($args); mode = [string]$PSNativeCommandArgumentPassing }"

type wrappedArgumentsResult struct {
	Arguments []string `json:"arguments"`
	Mode      string   `json:"mode"`
}

func appendPowerShellProfile(t *testing.T, home, body string) {
	t.Helper()
	profile := filepath.Join(home, "powershell", "Microsoft.PowerShell_profile.ps1")
	file, err := os.OpenFile(profile, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, writeErr := file.WriteString(body)
	closeErr := file.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
}

func TestWrappedArgumentsHelper(t *testing.T) {
	if os.Getenv(wrappedArgumentsHelperEnv) != "1" {
		return
	}
	if os.Getenv("LEAPMUX_WRAPPED_ARGUMENTS_FAILURE") == "1" {
		os.Exit(23)
	}
	for index, arg := range os.Args {
		if arg == "--" {
			if err := json.NewEncoder(os.Stdout).Encode(wrappedArgumentsResult{
				Arguments: os.Args[index+1:], Mode: os.Getenv("LEAPMUX_WRAPPED_ARGUMENT_MODE"),
			}); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestPowerShellPreservesNativeArguments(t *testing.T) {
	for _, mode := range []string{"Legacy", "Standard"} {
		t.Run(mode, func(t *testing.T) {
			shell, home := testutil.RequirePowerShellArgumentPassing(t, mode, "LEAPMUX_WRAPPED_ARGUMENT_MODE")
			executable, err := os.Executable()
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(home, "matching.txt"), nil, 0o600))
			script := filepath.Join(home, "arguments [script].ps1")
			require.NoError(t, os.WriteFile(script, []byte(argumentsReportScript), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(home, "leapmux-script-wrapper.ps1"), []byte(argumentsReportScript), 0o600))
			appendPowerShellProfile(t, home, "Set-Alias -Name leapmux-arguments-alias -Value "+pwshQuote(executable)+"\n"+
				"function leapmux-arguments-function { "+argumentsReportScript+" }\n"+
				"Set-Alias -Name leapmux-function-alias -Value leapmux-arguments-function\n"+
				"Set-Alias -Name leapmux-script-alias -Value "+pwshQuote(script)+"\n")
			arguments := []string{"", "plain", "a path with spaces", `a"b`, `a "quoted" path`, `a\"b`, `a\\"b`, `C:\a path\`, `C:\a path\\`, "one\ttwo", "one\ntwo", "it's $HOME; `literal`", "*.txt", "~", "日本語", "a\u2018b", "a\u2019b", "a\u201ab", "a\u201bb", strings.Repeat("x", 16384)}
			for _, target := range []struct {
				program string
				native  bool
			}{
				{executable, true}, {"leapmux-arguments-alias", true},
				{"leapmux-arguments-function", false}, {"leapmux-function-alias", false},
				{script, false}, {"leapmux-script-alias", false}, {"leapmux-script-wrapper", false},
			} {
				t.Run(filepath.Base(target.program), func(t *testing.T) {
					for _, withOptionalArgs := range []bool{false, true} {
						t.Run(map[bool]string{false: "simple", true: "conditional"}[withOptionalArgs], func(t *testing.T) {
							spec := WrapSpec{
								Shell: shell, Launch: Spec{Program: target.program, PrefixArgs: []string{"-test.run=^TestWrappedArgumentsHelper$", "--"}},
								BaseArgs: arguments, WorkingDir: home,
							}
							if !target.native {
								spec.Launch.PrefixArgs = nil
							}
							want := append([]string{}, arguments...)
							if withOptionalArgs {
								spec.EnvGated = testEnvGate(arguments...)
								want = append(want, arguments...)
							}
							cmd, delimiter, _ := Wrap(t.Context(), spec)
							cmd.Env = append(cmd.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home,
								"PATH="+home+string(os.PathListSeparator)+os.Getenv("PATH"),
								wrappedArgumentsHelperEnv+"=1", "LEAPMUX_TEST_GATE_A=", "LEAPMUX_TEST_GATE_B=")
							output, err := cmd.Output()
							require.NoError(t, err)
							_, body, found := strings.Cut(string(output), delimiter+"\n")
							require.True(t, found, "output: %q", output)
							var got wrappedArgumentsResult
							require.NoError(t, json.Unmarshal([]byte(body), &got))
							assert.Equal(t, want, got.Arguments)
							assert.Equal(t, mode, got.Mode, "the profile must select the tested mode")
						})
					}
				})
			}
		})
	}
}

func TestPowerShellResolvesLiteralProgramPaths(t *testing.T) {
	for _, mode := range []string{"Legacy", "Standard"} {
		t.Run(mode, func(t *testing.T) {
			shell, home := testutil.RequirePowerShellArgumentPassing(t, mode, "LEAPMUX_WRAPPED_ARGUMENT_MODE")
			executable, err := os.Executable()
			require.NoError(t, err)
			for _, name := range []string{"agent [", "agent ]", "agent ?", "agent *", "agent `", "agent [literal] []?*`", "agent ‘quoted’"} {
				t.Run(name, func(t *testing.T) {
					program := filepath.Join(home, name)
					require.NoError(t, os.Symlink(executable, program))
					cmd, delimiter, _ := Wrap(t.Context(), WrapSpec{
						Shell: shell, Launch: Spec{Program: program},
						BaseArgs: []string{"-test.run=^TestWrappedArgumentsHelper$", "--", "literal path"}, WorkingDir: home,
					})
					cmd.Env = append(cmd.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home, wrappedArgumentsHelperEnv+"=1")
					output, err := cmd.CombinedOutput()
					require.NoError(t, err, "output: %s", output)
					_, body, found := strings.Cut(string(output), delimiter+"\n")
					require.True(t, found, "output: %s", output)
					var got wrappedArgumentsResult
					require.NoError(t, json.Unmarshal([]byte(body), &got))
					assert.Equal(t, []string{"literal path"}, got.Arguments)
				})
			}
		})
	}
}

func TestPowerShellPreservesNativeExitStatus(t *testing.T) {
	for _, mode := range []string{"Legacy", "Standard"} {
		t.Run(mode, func(t *testing.T) {
			shell, home := testutil.RequirePowerShellArgumentPassing(t, mode, "LEAPMUX_WRAPPED_ARGUMENT_MODE")
			executable, err := os.Executable()
			require.NoError(t, err)
			appendPowerShellProfile(t, home,
				"Set-Alias -Name leapmux-cycle-first -Value leapmux-cycle-second\n"+
					"Set-Alias -Name leapmux-cycle-second -Value LEAPMUX-CYCLE-FIRST\n"+
					"Set-Alias -Name leapmux-unresolved-alias -Value "+pwshQuote(filepath.Join(home, "missing-target"))+"\n")
			for _, tc := range []struct {
				name     string
				program  string
				wantCode int
			}{
				{"native failure", executable, 23},
				{"missing command", filepath.Join(home, "missing-command"), 127},
				{"cyclic aliases", "leapmux-cycle-first", 127},
				{"unresolved alias", "leapmux-unresolved-alias", 127},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					cmd, _, _ := Wrap(ctx, WrapSpec{
						Shell: shell, Launch: Spec{Program: tc.program},
						BaseArgs: []string{"-test.run=^TestWrappedArgumentsHelper$", "--"}, WorkingDir: home,
					})
					cmd.Env = append(cmd.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home,
						wrappedArgumentsHelperEnv+"=1", "LEAPMUX_WRAPPED_ARGUMENTS_FAILURE=1")
					output, err := cmd.CombinedOutput()
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr, "output: %s", output)
					assert.Equal(t, tc.wantCode, exitErr.ExitCode(), "output: %s", output)
				})
			}
		})
	}
}

func TestPowerShellPreservesEmptyArgumentLists(t *testing.T) {
	for _, mode := range []string{"Legacy", "Standard"} {
		t.Run(mode, func(t *testing.T) {
			shell, home := testutil.RequirePowerShellArgumentPassing(t, mode, "LEAPMUX_WRAPPED_ARGUMENT_MODE")
			appendPowerShellProfile(t, home, "function leapmux-empty-arguments { "+argumentsReportScript+" }\n")
			script := filepath.Join(home, "empty arguments.ps1")
			require.NoError(t, os.WriteFile(script, []byte(argumentsReportScript), 0o600))
			for _, program := range []string{"/bin/sh", "leapmux-empty-arguments", script} {
				t.Run(filepath.Base(program), func(t *testing.T) {
					_, output := runWrapped(t, WrapSpec{
						Shell: shell, Launch: Spec{Program: program}, WorkingDir: home,
					}, "HOME="+home, "XDG_CONFIG_HOME="+home)
					if program == "/bin/sh" {
						assert.Empty(t, output)
						return
					}
					var got wrappedArgumentsResult
					require.NoError(t, json.Unmarshal([]byte(output), &got))
					assert.Equal(t, []string{}, got.Arguments)
					assert.Equal(t, mode, got.Mode)
				})
			}
		})
	}
}
