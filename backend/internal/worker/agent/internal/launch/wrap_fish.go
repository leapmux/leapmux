package launch

import (
	"fmt"
	"strings"
)

func fishClearEnv(keys []string) string {
	var body strings.Builder
	for _, key := range keys {
		// An unexported global shadows a universal without changing its saved value.
		body.WriteString("builtin set --global --unexport " + key + "; or builtin exit $status; ")
	}
	return body.String()
}

func fishSetEnv(pins []environmentPin) string {
	var body strings.Builder
	for _, pin := range pins {
		body.WriteString("builtin set --global --export " + pin.target + " \"$" + pin.source + "\"; or builtin exit $status; ")
		body.WriteString("builtin set --global --unexport " + pin.source + "; or builtin exit $status; ")
	}
	return body.String()
}

func buildFishCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	// The step runs after the profile and before StripEnvKeys and the pins, so
	// a PATH that a provider pins wins (see fishReprependControlCLI).
	script.WriteString(fishReprependControlCLI + fishClearEnv(spec.StripEnvKeys) + fishSetEnv(spec.pins))
	if len(prepared.candidates) != 0 {
		script.WriteString(fishLookupSetup)
		fmt.Fprintf(&script, "set -l __lm_selected\nfor __lm_candidate in %s\nset __lm_selected \"$__lm_candidate\"\n__leapmux_lookup \"$__lm_selected\" command\nif test \"$__lm_kind\" != missing; break; end\nend\n", strings.Join(quotedArguments(prepared.candidates, fishQuote), " "))
		fmt.Fprintf(&script, "if test \"$__lm_kind\" = missing; builtin printf '\\n%%s\\n' %s >&2; builtin exit 127; end\n", fishQuote(prepared.exhaustedMarker))
	} else {
		fmt.Fprintf(&script, "set __lm_kind external\nset __lm_path %s\n", fishQuote(spec.Launch.Program))
	}
	fmt.Fprintf(&script, "set -l __lm_args %s\n", strings.Join(quotedArguments(spec.BaseArgs, fishQuote), " "))
	if gate := spec.EnvGated; gate != nil {
		conditions := make([]string, len(gate.EnvVars))
		for index, name := range gate.EnvVars {
			conditions[index] = `test -n "$` + name + `"`
		}
		fmt.Fprintf(&script, "if %s\n%selse\n%sset -a __lm_args %s\nend\n",
			strings.Join(conditions, "; or "), protocolLine("fish", prepared.MetaPrefix+gate.MetaKey+"=false", true),
			protocolLine("fish", prepared.MetaPrefix+gate.MetaKey+"=true", true), strings.Join(quotedArguments(gate.Args, fishQuote), " "))
	}
	if len(prepared.environmentProbe) == 0 {
		script.WriteString(protocolLine("fish", prepared.Delimiter, spec.EnvGated == nil))
	} else {
		fmt.Fprintf(&script, "if test \"$__lm_kind\" = external; and command %s\n%selse\n%send\n",
			strings.Join(quotedArguments(prepared.environmentProbe, fishQuote), " "),
			protocolLine("fish", prepared.Delimiter, false), protocolLine("fish", prepared.Delimiter, spec.EnvGated == nil))
	}
	script.WriteString("if test \"$__lm_kind\" = external; exec \"$__lm_path\" $__lm_args; else; $__lm_selected $__lm_args; end\nbuiltin exit $status\n")
	return script.String()
}
