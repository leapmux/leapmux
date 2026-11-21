package launch

import (
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// powerShellModules are the qualifiers that the generated PowerShell may write
// before a cmdlet. PowerShell runs an alias or a function before a cmdlet of
// the same name, and a profile can define either. A module-qualified name
// reaches the cmdlet.
var powerShellModules = []string{`Microsoft.PowerShell.Core\`, `Microsoft.PowerShell.Management\`, `Microsoft.PowerShell.Utility\`}

// powerShellKeywords are the keywords that start a statement of the
// generated PowerShell. No profile can define a command with such a name.
var powerShellKeywords = []string{"if", "elseif", "else", "foreach", "while", "function", "return", "exit", "break", "continue"}

// powerShellString matches a single-quoted or a double-quoted string, whose
// text is never a command.
var powerShellString = regexp.MustCompile("'(?:[^']|'')*'|\"(?:[^\"`]|`.)*\"")

// powerShellCommandWord matches the first word at each position where
// PowerShell reads a command: the start of a line or a statement, a block, a
// subexpression, a pipeline stage, the right side of an assignment, and the
// operand of the call operator. The word can also be `%` or `?`, the aliases of
// ForEach-Object and Where-Object. The second group matches the `=` after a
// hashtable key, which is not a command.
var powerShellCommandWord = regexp.MustCompile(`(?m)(?:^|[;{}(|=&])[ \t]*([A-Za-z_][\w.\\-]*|[%?])([ \t]*=(?:[^=]|$))?`)

// powerShellFunctionName matches the name in a function definition.
var powerShellFunctionName = regexp.MustCompile(`\bfunction[ \t]+([A-Za-z_][\w-]*)`)

// powerShellCommands returns each command that script calls, and each of
// them that a profile can replace: a word that is no keyword, no function
// that script defines, and no cmdlet with a module qualifier.
func powerShellCommands(script string) (commands, unqualified []string) {
	code := powerShellString.ReplaceAllString(script, "''")
	var defined []string
	for _, match := range powerShellFunctionName.FindAllStringSubmatch(code, -1) {
		defined = append(defined, match[1])
	}
	for _, match := range powerShellCommandWord.FindAllStringSubmatch(code, -1) {
		word := match[1]
		if match[2] != "" || slices.Contains(powerShellKeywords, word) {
			continue
		}
		commands = append(commands, word)
		qualified := slices.ContainsFunc(powerShellModules, func(module string) bool { return strings.HasPrefix(word, module) })
		if !qualified && !slices.Contains(defined, word) {
			unqualified = append(unqualified, word)
		}
	}
	return commands, unqualified
}

// The scanner finds a command at each position where PowerShell reads one, so
// a pass of the test below means something.
func TestPowerShellCommandsFindsEachCommandPosition(t *testing.T) {
	t.Parallel()
	commands, unqualified := powerShellCommands("function own($x) { return @{ Key = $x; Other = 1 } }\n" +
		"Remove-Item 'Env:A; Set-Item' -ErrorAction SilentlyContinue; $a = Join-Path 'x' 'y'\n" +
		"if ($x) { Write-Output 'z' } else { Test-Path (Get-Item x) }\n" +
		"$b = @($c | Where-Object { $_ -cne 1 } | % { $_ }) -join ';'\n" +
		"own 1; & Get-Command; Microsoft.PowerShell.Management\\Remove-Item -LiteralPath 'Env:A'\n")
	assert.Equal(t, []string{"Remove-Item", "Join-Path", "Write-Output", "Test-Path", "Get-Item", "Where-Object", "%", "own", "Get-Command", `Microsoft.PowerShell.Management\Remove-Item`}, commands)
	assert.Equal(t, []string{"Remove-Item", "Join-Path", "Write-Output", "Test-Path", "Get-Item", "Where-Object", "%", "Get-Command"}, unqualified)
}

// Each command that the generated PowerShell calls is a keyword, a function
// that the command defines itself, or a cmdlet with its module qualifier, so
// no profile function or alias runs in its place.
func TestPowerShellCommandsCallNoUnqualifiedCmdlet(t *testing.T) {
	t.Parallel()
	var found []string
	check := func(name, script string) {
		commands, unqualified := powerShellCommands(script)
		assert.Empty(t, unqualified, "%s calls a command that a profile can replace:\n%s", name, script)
		found = append(found, commands...)
	}
	for _, shell := range []string{"/usr/bin/pwsh", "/usr/bin/powershell"} {
		for _, launch := range []Spec{{Program: "agent"}, ShellCommands("leapmux-test-missing", "leapmux-test-agent")} {
			for _, gate := range []*EnvGatedArgs{nil, testEnvGate("gated")} {
				cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{
					Shell: shell, Launch: launch, BaseArgs: []string{"base"},
					StripEnvKeys: []string{"LEAPMUX_TEST_STRIP"}, SetEnv: []string{"LEAPMUX_TEST_PIN=pinned"},
					EnvGated: gate, WorkingDir: "/tmp",
				})
				check(shell, renderedWrapScript(t, cmd))
			}
		}
	}
	for _, native := range []bool{false, true} {
		check("the probe", renderCommandProbe("pwsh", "__LEAPMUX_PROBE_TEST__", []string{"leapmux-test-agent"}, native, false))
	}
	// The strip, the pins and the control CLI step call these cmdlets, so the
	// scan reaches each of them.
	assert.Contains(t, found, `Microsoft.PowerShell.Management\Remove-Item`)
	assert.Contains(t, found, `Microsoft.PowerShell.Core\Where-Object`)
}

// A PowerShell profile can define a function with the name of a cmdlet, and
// PowerShell runs the function in its place. The launch strips each key and
// filters PATH through module-qualified cmdlets, so the strip, the pins and
// the control CLI step act past such functions.
func TestWrapPowerShellCallsTheCmdletsPastProfileFunctions(t *testing.T) {
	interpreters := []string{"pwsh"}
	if runtime.GOOS == "windows" {
		interpreters = append(interpreters, "powershell")
	}
	executable := profileCommandExecutable(t)
	for _, name := range interpreters {
		t.Run(name, func(t *testing.T) {
			shell := requireProfileShell(t, name, name)
			home := t.TempDir()
			link := filepath.Join(t.TempDir(), "cli-1")
			decoy := filepath.Join(t.TempDir(), "decoy")
			quote := shellquote.PowerShell.Quote
			cmd, delimiter, _ := wrapTestCommand(t, testutil.DeadlineContext(t), WrapSpec{
				Shell: shell, Launch: Spec{Program: executable}, BaseArgs: []string{"-test.run=^TestPinnedEnvironmentChild$"},
				StripEnvKeys: []string{"LEAPMUX_TEST_STRIPPED"}, SetEnv: []string{"PRIVATE_TOKEN=pinned"}, WorkingDir: home,
			})
			cmd.Env = controlenv.Apply(envutil.PinEnv(cmd.Environ(),
				"POWERSHELL_TELEMETRY_OPTOUT=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1",
				"LEAPMUX_PIN_TEST_HELPER=1", "LEAPMUX_TEST_STRIPPED=inherited"),
				[]string{controlenv.CLIVariable + "=" + filepath.Join(link, "leapmux")})
			// The profile moves a decoy before the Worker's directory, as a
			// real profile can, so the control CLI step filters PATH.
			prependPowerShellProfile(cmd, "$env:PATH = "+quote(decoy)+" + [System.IO.Path]::PathSeparator + $env:PATH\n"+
				"function Remove-Item { [Console]::Out.WriteLine('profile-remove-item') }\n"+
				"function Where-Object { [Console]::Out.WriteLine('profile-where-object') }\n")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			text := strings.ReplaceAll(string(output), "\r\n", "\n")
			assert.NotContains(t, text, "profile-", "no profile function runs")
			require.Contains(t, text, delimiter+"\n")

			result := readPinResult(t, output)
			assert.False(t, result.StrippedPresent, "the launch strips the variable")
			assert.Equal(t, "pinned", result.Value)
			assert.Empty(t, result.Transport)
			entries := filepath.SplitList(result.Path)
			require.NotEmpty(t, entries)
			assert.Equal(t, link, entries[0], "the directory comes first")
			assert.Contains(t, entries, decoy, "the step keeps every other entry")
		})
	}
}
