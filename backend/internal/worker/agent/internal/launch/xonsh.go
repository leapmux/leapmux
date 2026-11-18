package launch

import (
	"fmt"
	"strconv"
	"strings"
)

const xonshLookupSetup = `
import os as __lm_os
def __leapmux_lookup(name, native_only=False):
    if not native_only and name in __xonsh__.aliases:
        return ("callable", name)
    path = __xonsh__.commands_cache.locate_binary(name, ignore_alias=True)
    if path and __lm_os.path.isfile(path) and __lm_os.access(path, __lm_os.X_OK):
        if not native_only or __lm_os.name != "nt" or __lm_os.path.splitext(path)[1].lower() in (".exe", ".com"):
            return ("external", __lm_os.path.abspath(path))
    return None
`

func pythonString(value string) string { return strconv.Quote(value) }

func xonshProbeResult(name, present, absent string, nativeOnly bool) string {
	if nativeOnly {
		return fmt.Sprintf("__lm_result = __leapmux_lookup(%s, True)\nprint(%s + __lm_result[1] if __lm_result else %s, flush=True)\n",
			pythonString(name), pythonString(present+":"), pythonString(absent))
	}
	return fmt.Sprintf("print(%s if __leapmux_lookup(%s) else %s, flush=True)\n",
		pythonString(present), pythonString(name), pythonString(absent))
}

func pythonArguments(args []string) string {
	return "[" + strings.Join(quotedArguments(args, pythonString), ", ") + "]"
}

func buildXonshCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	script.WriteString(xonshLookupSetup)
	script.WriteString("import sys as __lm_sys\n$XONSH_INTERACTIVE = False\n")
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
		fmt.Fprintf(&script, "if __lm_selected is None:\n    print('\\n' + %s, file=__lm_sys.stderr, flush=True)\n    raise SystemExit(127)\n", pythonString(prepared.exhaustedMarker))
	}
	fmt.Fprintf(&script, "__lm_args = %s\n", pythonArguments(spec.BaseArgs))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "if any(__xonsh__.env.get(name) for name in %s):\n    print('\\n' + %s, flush=True)\nelse:\n    print('\\n' + %s, flush=True)\n    __lm_args.extend(%s)\n",
			pythonArguments(gate.EnvVars), pythonString(prepared.MetaPrefix+gate.MetaKey+"=false"),
			pythonString(prepared.MetaPrefix+gate.MetaKey+"=true"), pythonArguments(gate.Args))
	}
	script.WriteString(probeLine("xonsh", prepared.Delimiter))
	script.WriteString("raise SystemExit(![@([__lm_selected[1]] + __lm_args)].returncode)\n")
	return script.String()
}
