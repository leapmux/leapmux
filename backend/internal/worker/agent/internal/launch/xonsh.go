package launch

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// xonshLookupSetup defines the functions that every xonsh command of the
// package uses. The rc file of xonsh runs in the global namespace of the
// command, so a profile can define a function with the name of a Python
// builtin, such as any or next. The command calls each builtin through
// __lm_builtins, and it writes through __leapmux_write, never through print.
const xonshLookupSetup = `
import builtins as __lm_builtins
import os as __lm_os
import sys as __lm_sys

def __leapmux_write(stream, line):
    stream.write(line + "\n")
    stream.flush()

def __leapmux_lookup(name, native_only=False):
    if not native_only and name in __xonsh__.aliases:
        return ("callable", name)
    env = __xonsh__.env
    filenames = (name,)
    if __lm_os.name == "nt":
        extensions = {extension.lower() for extension in env.get("PATHEXT", ())}
        if native_only:
            extensions.intersection_update((".exe", ".com"))
        filenames = __lm_builtins.tuple(
            filename for filename in __xonsh__.commands_cache.get_possible_names(name)
            if __lm_os.path.splitext(filename)[1].lower() in extensions
        )
    # The command cache can still contain paths removed by this profile or
    # replaced by the launch environment. Search the current PATH in order.
    for directory in env.get("PATH", ()):
        for filename in filenames:
            path = __lm_os.path.join(directory, filename)
            if __lm_os.path.isfile(path) and __lm_os.access(path, __lm_os.X_OK):
                return ("external", __lm_os.path.abspath(path))
    return None
`

// xonshEnvironmentSetup defines __LeapmuxExactPath, through which the control
// CLI step stores PATH, and __leapmux_pin, which sets one pin after the
// profile. A program receives the string that the detype method of the xonsh
// environment gives for each variable: `![...]` passes those strings, and
// replace_env puts them into os.environ for a profile alias. xonsh converts a
// value when it stores the value and again in detype, so a plain assignment
// can change a value:
//
//   - xonsh stores a variable whose name ends in PATH or DIRS as an EnvPath,
//     a list of paths, and detype expands `~` and `$NAME` in each entry. A
//     __LeapmuxExactPath is an EnvPath that returns each entry unchanged, so
//     the entries join back to the exact value. A PATH stored this way stays
//     a list, which the candidate lookup and the command lookup of xonsh
//     need. Both then read each entry as the program receives it, without
//     expansion.
//   - xonsh converts the value of one of its own settings, such as the bool
//     AUTO_CD. The pin registers such a variable as a plain string first.
//
// Registering every pinned variable as a string would be simpler, but a
// pinned PATH would then be a string, and each lookup would iterate its
// characters. A program that the launch starts with its own copy of the
// environment would receive exact values too, but a profile alias starts its
// programs through the environment of xonsh.
//
// The name of a transport variable ends in a digit or "_" (see
// prepareShellLaunch), so no PATH or DIRS pattern applies to it, and pop
// returns its value as the environment held it.
//
// buildXonshCommand writes the setup into every launch, because the control
// CLI step runs in every launch.
const xonshEnvironmentSetup = `
from xonsh.environ import EnvPath as __lm_EnvPath
from xonsh.tools import env_path_to_str as __lm_env_path_to_str

class __LeapmuxExactPath(__lm_EnvPath):
    def __getitem__(self, item):
        return self._l[item]

def __leapmux_pin(target, source):
    env = __xonsh__.env
    value = env.pop(source)
    detype = env.get_detyper(target)
    if detype is __lm_env_path_to_str:
        value = __LeapmuxExactPath(value)
    elif detype is None or not env.get_validator(target)(value) or detype(value) != value:
        env.register(target, type="str")
    env[target] = value
`

// pythonString renders value as a Python string literal, which xonsh reads in
// Python mode. Python mode substitutes nothing inside a plain string literal:
// `$HOME`, `@(...)`, `!(...)`, `{}` and backquotes stay as they are.
//
// The literal holds printable ASCII only. xonsh fails to start on a byte of
// its arguments that is not valid UTF-8: its setproctitle module raises
// UnicodeDecodeError, and xonsh then runs another shell in its place.
//
//   - A quote and a backslash take a backslash.
//   - Another printable ASCII character stays as it is.
//   - An ASCII control character takes \xNN, and a character outside ASCII
//     takes \UNNNNNNNN. Each escape has a fixed length, so the character
//     after it cannot extend it.
//   - A byte that is not part of valid UTF-8 takes \U0000dcNN, the surrogate
//     U+DCNN. Python decodes such a byte of an argument or an environment
//     value to this surrogate, and encodes the surrogate back to the byte
//     when it starts a program. Go's strconv.Quote writes the byte as \xNN,
//     which Python reads as the character U+00NN, so a program would receive
//     the UTF-8 encoding of that character instead.
func pythonString(value string) string {
	var literal strings.Builder
	literal.Grow(len(value) + 2)
	literal.WriteByte('"')
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
		switch {
		case character == utf8.RuneError && size == 1:
			fmt.Fprintf(&literal, `\U0000dc%02x`, value[index])
		case character == '"' || character == '\\':
			literal.WriteByte('\\')
			literal.WriteRune(character)
		case character >= 0x20 && character < 0x7f:
			literal.WriteRune(character)
		case character < 0x80:
			fmt.Fprintf(&literal, `\x%02x`, character)
		default:
			fmt.Fprintf(&literal, `\U%08x`, character)
		}
		index += size
	}
	literal.WriteByte('"')
	return literal.String()
}

func xonshProbeResult(name, present, absent string, nativeOnly bool) string {
	if nativeOnly {
		return fmt.Sprintf("__lm_result = __leapmux_lookup(%s, True)\n__leapmux_write(__lm_sys.stdout, %s + __lm_result[1] if __lm_result else %s)\n",
			pythonString(name), pythonString(present+":"), pythonString(absent))
	}
	return fmt.Sprintf("__leapmux_write(__lm_sys.stdout, %s if __leapmux_lookup(%s) else %s)\n",
		pythonString(present), pythonString(name), pythonString(absent))
}

// pythonArguments renders args as a Python list of string literals.
func pythonArguments(args []string) string {
	return "[" + strings.Join(quotedArguments(args, pythonString), ", ") + "]"
}

// buildXonshCommand builds the inner command string for xonsh.
//
// `xonsh -c` prints the value of each expression statement outside a
// function, also inside a loop or an if statement. Each statement outside a
// function is therefore an assignment, a compound statement, or a call that
// returns None, which xonsh does not print.
func buildXonshCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	script.WriteString(xonshLookupSetup)
	script.WriteString(xonshEnvironmentSetup)
	script.WriteString("$XONSH_INTERACTIVE = False\n")
	// The step reads __lm_os from xonshLookupSetup and __LeapmuxExactPath from
	// xonshEnvironmentSetup. It runs before StripEnvKeys and the pins, so a
	// PATH that a provider pins wins (see xonshReprependControlCLI).
	script.WriteString(xonshReprependControlCLI)
	if len(spec.StripEnvKeys) != 0 {
		// pop returns the value that it removes. The assignment keeps each
		// stripped value, such as a credential, off stdout.
		fmt.Fprintf(&script, "for __lm_key in %s:\n    __lm_stripped = __xonsh__.env.pop(__lm_key, None)\n", pythonArguments(spec.StripEnvKeys))
	}
	for _, pin := range spec.pins {
		fmt.Fprintf(&script, "__leapmux_pin(%s, %s)\n", pythonString(pin.target), pythonString(pin.source))
	}
	if len(prepared.candidates) == 0 {
		fmt.Fprintf(&script, "__lm_selected = ('external', %s)\n", pythonString(spec.Launch.Program))
	} else {
		fmt.Fprintf(&script, "__lm_selected = __lm_builtins.next((result for name in %s if (result := __leapmux_lookup(name))), None)\n", pythonArguments(prepared.candidates))
		fmt.Fprintf(&script, "if __lm_selected is None:\n    __leapmux_write(__lm_sys.stderr, %s)\n    raise __lm_builtins.SystemExit(127)\n", pythonString("\n"+prepared.exhaustedMarker))
		// Python subprocesses inside profile aliases inherit os.environ.
		// Give them the same environment as native Xonsh subprocesses.
		script.WriteString("if __lm_selected[0] == 'callable':\n    __xonsh__.env.replace_env()\n")
	}
	fmt.Fprintf(&script, "__lm_args = %s\n", pythonArguments(spec.BaseArgs))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "if __lm_builtins.any(__xonsh__.env.get(name) for name in %s):\n    __leapmux_write(__lm_sys.stdout, %s)\nelse:\n    __leapmux_write(__lm_sys.stdout, %s)\n    __lm_args.extend(%s)\n",
			pythonArguments(gate.EnvVars), pythonString("\n"+prepared.MetaPrefix+gate.MetaKey+"=false"), pythonString("\n"+prepared.MetaPrefix+gate.MetaKey+"=true"), pythonArguments(gate.Args))
	}
	if len(prepared.environmentProbe) == 0 {
		script.WriteString(protocolLine("xonsh", prepared.Delimiter, spec.EnvGated == nil))
	} else {
		script.WriteString("import subprocess as __lm_subprocess\n__lm_observed = False\nif __lm_selected[0] == 'external':\n    __lm_sys.stdout.flush()\n    try:\n")
		fmt.Fprintf(&script, "        __lm_observed = (__lm_subprocess.call(%s, env=__xonsh__.env.detype()) == 0)\n", pythonArguments(prepared.environmentProbe))
		script.WriteString("    except __lm_builtins.OSError:\n        pass\n")
		fmt.Fprintf(&script, "if __lm_observed:\n    %selse:\n    %s",
			protocolLine("xonsh", prepared.Delimiter, false), protocolLine("xonsh", prepared.Delimiter, spec.EnvGated == nil))
	}
	script.WriteString("raise __lm_builtins.SystemExit(![@([__lm_selected[1]] + __lm_args)].returncode)\n")
	return script.String()
}
