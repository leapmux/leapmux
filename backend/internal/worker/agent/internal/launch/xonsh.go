package launch

import (
	"fmt"
	"strconv"
	"strings"
)

const xonshLookupSetup = `
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
        filenames = tuple(
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

func pythonString(value string) string { return strconv.Quote(value) }

func xonshProbeResult(name, present, absent string, nativeOnly bool) string {
	if nativeOnly {
		return fmt.Sprintf("__lm_result = __leapmux_lookup(%s, True)\n__leapmux_write(__lm_sys.stdout, %s + __lm_result[1] if __lm_result else %s)\n",
			pythonString(name), pythonString(present+":"), pythonString(absent))
	}
	return fmt.Sprintf("__leapmux_write(__lm_sys.stdout, %s if __leapmux_lookup(%s) else %s)\n",
		pythonString(present), pythonString(name), pythonString(absent))
}

func pythonArguments(args []string) string {
	return "[" + strings.Join(quotedArguments(args, pythonString), ", ") + "]"
}

func buildXonshCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	script.WriteString(xonshLookupSetup)
	script.WriteString("$XONSH_INTERACTIVE = False\n")
	// The step reads __lm_os from xonshLookupSetup. It runs before
	// StripEnvKeys and the pins, so a PATH that a provider pins wins (see
	// xonshReprependControlCLI).
	script.WriteString(xonshReprependControlCLI)
	for _, key := range spec.StripEnvKeys {
		fmt.Fprintf(&script, "__xonsh__.env.pop(%s, None)\n", pythonString(key))
	}
	for _, pin := range spec.pins {
		fmt.Fprintf(&script, "__xonsh__.env[%s] = __xonsh__.env.pop(%s)\n", pythonString(pin.target), pythonString(pin.source))
	}
	if len(prepared.candidates) == 0 {
		fmt.Fprintf(&script, "__lm_selected = ('external', %s)\n", pythonString(spec.Launch.Program))
	} else {
		fmt.Fprintf(&script, "__lm_selected = next((result for name in %s if (result := __leapmux_lookup(name))), None)\n", pythonArguments(prepared.candidates))
		fmt.Fprintf(&script, "if __lm_selected is None:\n    __leapmux_write(__lm_sys.stderr, %s)\n    raise SystemExit(127)\n", pythonString("\n"+prepared.exhaustedMarker))
		// Python subprocesses inside profile aliases inherit os.environ.
		// Give them the same environment as native Xonsh subprocesses.
		script.WriteString("if __lm_selected[0] == 'callable':\n    __xonsh__.env.replace_env()\n")
	}
	fmt.Fprintf(&script, "__lm_args = %s\n", pythonArguments(spec.BaseArgs))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "if any(__xonsh__.env.get(name) for name in %s):\n    __leapmux_write(__lm_sys.stdout, %s)\nelse:\n    __leapmux_write(__lm_sys.stdout, %s)\n    __lm_args.extend(%s)\n",
			pythonArguments(gate.EnvVars), pythonString("\n"+prepared.MetaPrefix+gate.MetaKey+"=false"), pythonString("\n"+prepared.MetaPrefix+gate.MetaKey+"=true"), pythonArguments(gate.Args))
	}
	if len(prepared.environmentProbe) == 0 {
		script.WriteString(protocolLine("xonsh", prepared.Delimiter, spec.EnvGated == nil))
	} else {
		script.WriteString("import subprocess as __lm_subprocess\n__lm_observed = False\nif __lm_selected[0] == 'external':\n    __lm_sys.stdout.flush()\n    try:\n")
		fmt.Fprintf(&script, "        __lm_observed = (__lm_subprocess.call(%s, env=__xonsh__.env.detype()) == 0)\n", pythonArguments(prepared.environmentProbe))
		script.WriteString("    except OSError:\n        pass\n")
		fmt.Fprintf(&script, "if __lm_observed:\n    %selse:\n    %s",
			protocolLine("xonsh", prepared.Delimiter, false), protocolLine("xonsh", prepared.Delimiter, spec.EnvGated == nil))
	}
	script.WriteString("raise SystemExit(![@([__lm_selected[1]] + __lm_args)].returncode)\n")
	return script.String()
}
