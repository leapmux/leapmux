package launch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// pythonString writes printable ASCII only, and each escape has a fixed
// length. A byte that is not valid UTF-8 becomes the surrogate that Python
// decodes the byte to, so a program receives the byte again.
func TestPythonString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"an empty string", "", `""`},
		{"plain text", "plain text", `"plain text"`},
		{"a quote", `a"b`, `"a\"b"`},
		{"an apostrophe", "a'b", `"a'b"`},
		{"a backslash", `a\b`, `"a\\b"`},
		{"text that spells an escape", `\x80 \U0000dc80`, `"\\x80 \\U0000dc80"`},
		{"xonsh syntax", "$HOME ${HOME} {a,b} @(x) !(y) $(z) `glob` ~ *", "\"$HOME ${HOME} {a,b} @(x) !(y) $(z) `glob` ~ *\""},
		{"a NUL", "\x00", `"\x00"`},
		{"control characters", "\t\n\r\x1b\x7f", `"\x09\x0a\x0d\x1b\x7f"`},
		{"a hex digit after a short escape", "\x012", `"\x012"`},
		{"characters outside ASCII", "é한😀", `"\U000000e9\U0000d55c\U0001f600"`},
		{"a hex digit after a long escape", "é1", `"\U000000e91"`},
		{"the character U+0080", "\u0080", `"\U00000080"`},
		{"the replacement character", "�", `"\U0000fffd"`},
		{"bytes that are not valid UTF-8", "a\x80\xffb", `"a\U0000dc80\U0000dcffb"`},
		{"a truncated sequence", "\xe2\x82", `"\U0000dce2\U0000dc82"`},
		{"an encoded surrogate", "\xed\xa0\x80", `"\U0000dced\U0000dca0\U0000dc80"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, pythonString(tc.value))
		})
	}
}

// xonsh fails to start on a byte of its arguments that is not valid UTF-8
// (see pythonString). Each command that the package gives xonsh holds
// printable ASCII and newlines only, whatever the program, the arguments, the
// candidates, the env gate and the probe names hold.
func TestXonshCommandsHoldPrintableASCIIOnly(t *testing.T) {
	t.Parallel()
	// A native probe name holds no slash or backslash (see
	// validNativeProgramName).
	const name = "tab\t ctrl\x01 del\x7f quote\" apostrophe' $HOME ${HOME} {} @(x) !(y) $(z) `q` ~ 한 \u0080 \x80\xff"
	const text = name + ` back\slash`
	for _, launch := range []Spec{
		{Program: "/opt/" + name + "/agent", PrefixArgs: []string{text}},
		ShellCommands("leapmux-test-missing", "leapmux-test-agent"),
	} {
		for _, gate := range []*EnvGatedArgs{nil, testEnvGate(text, "")} {
			cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{
				Shell: "/usr/bin/xonsh", Launch: launch, BaseArgs: []string{text, ""},
				StripEnvKeys: []string{"LEAPMUX_TEST_STRIP"}, SetEnv: []string{"LEAPMUX_TEST_PIN=" + text},
				EnvGated: gate, WorkingDir: "/tmp",
			})
			assertPrintableASCII(t, renderedWrapScript(t, cmd))
		}
	}
	for _, native := range []bool{false, true} {
		assertPrintableASCII(t, renderCommandProbe("xonsh", "__LEAPMUX_PROBE_TEST__", []string{name}, native, false))
	}
}

// assertPrintableASCII fails for the first byte of script that is neither a
// newline nor printable ASCII.
func assertPrintableASCII(t *testing.T, script string) {
	t.Helper()
	for index := range len(script) {
		if c := script[index]; c != '\n' && (c < 0x20 || c > 0x7e) {
			assert.Failf(t, "the script holds a byte outside printable ASCII",
				"byte %#02x at %d: %q", c, index, script[max(0, index-40):min(len(script), index+40)])
			return
		}
	}
}

func TestXonshEnvCheckUsesDetypedEnvironment(t *testing.T) {
	t.Parallel()
	cmd, _, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell:      "/usr/bin/xonsh",
		Launch:     Spec{Program: "/bin/sh"},
		EnvGated:   testEnvGate("--extra"),
		WorkingDir: "/tmp",
	})
	script := renderedWrapScript(t, cmd)
	assert.Contains(t, script, "__xonsh__.env.detype()")
	assert.NotContains(t, script, "__xonsh__.env.get(name)")
}

func TestElvishFollowsSymlinkProfileAndTracksObservedProbe(t *testing.T) {
	t.Parallel()
	assert.Contains(t, elvishLoadProfile, "&follow-symlink=$true")
	cmd, delimiter, _ := wrapTestCommand(t, t.Context(), WrapSpec{
		Shell:              "/usr/bin/elvish",
		Launch:             Spec{Program: "/bin/sh"},
		ObserveEnvironment: &ObservationRequest{Variables: []string{"LEAPMUX_TEST_OBS"}},
		WorkingDir:         "/tmp",
	})
	script := renderedWrapScript(t, cmd)
	assert.Contains(t, script, "__lm_builtin:printf \"%s\\n\" '"+delimiter+"'")
}
