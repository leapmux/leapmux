package launch

import (
	"fmt"
	"runtime"
	"strings"
)

const elvishLookupSetup = "use builtin __lm_builtin\nuse path __lm_path\nvar __lm_profile = $nil\n"

// Elvish's command mode skips rc.elv. Load it as a module so it retains its
// source path and relative imports. The first library directory is the lib
// directory beside rc.elv. This spec resolves to rc in its parent.
const elvishLoadProfile = `use os __lm_os
use runtime __lm_runtime
if (and (not (__lm_builtin:eq $__lm_runtime:rc-path $nil)) (__lm_os:is-regular &follow-symlink=$true $__lm_runtime:rc-path)) {
  if (__lm_os:exists (__lm_path:join (__lm_path:dir $__lm_runtime:rc-path) rc.so)) {
    __lm_builtin:fail 'Elvish cannot load the launch profile because a native module also uses its basename'
  }
  use leapmux-profile/../../rc __lm_loaded_profile
  set __lm_profile = $__lm_loaded_profile:
}
`

func elvishSetup(loginShell bool) string {
	if loginShell {
		return elvishLookupSetup + elvishLoadProfile
	}
	return elvishLookupSetup
}

func elvishQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (elvishDialect) protocolLine(line string, first bool) string {
	format := `"%s\n"`
	if first {
		format = `"\n%s\n"`
	}
	return "__lm_builtin:printf " + format + " " + elvishQuote(line) + "\n"
}

func (d elvishDialect) renderProbe(nonce string, names []string, nativeOnly, loginShell bool) string {
	return renderStandardProbe(elvishSetup(loginShell), d, elvishProbeResult, nonce, names, nativeOnly)
}

func elvishProbeResult(name, present, absent string, nativeOnly bool) string {
	condition := "(__lm_builtin:has-external " + elvishQuote(name) + ")"
	presentValue := elvishQuote(present)
	if nativeOnly {
		presentValue = elvishQuote(present+":") + "(__lm_path:abs (__lm_builtin:search-external " + elvishQuote(name) + "))"
	} else {
		condition = "(or " + elvishProfileCallable(elvishQuote(name+"~")) + " " + condition + ")"
	}
	return "if " + condition + " { __lm_builtin:echo " +
		presentValue + " } else { __lm_builtin:echo " + elvishQuote(absent) + " }\n"
}

func elvishProfileCallable(variable string) string {
	return "(and (not (__lm_builtin:eq $__lm_profile $nil)) (__lm_builtin:has-key $__lm_profile " + variable + "))"
}

func (d elvishDialect) buildCommand(spec shellLaunchSpec, prepared *PreparedLaunch) (string, error) {
	var script strings.Builder
	script.WriteString(elvishSetup(spec.LoginShell))
	script.WriteString(elvishReprependControlCLI)
	for _, key := range spec.StripEnvKeys {
		fmt.Fprintf(&script, "__lm_builtin:unset-env %s\n", elvishQuote(key))
	}
	for _, pin := range spec.pins {
		fmt.Fprintf(&script, "__lm_builtin:set-env %s (__lm_builtin:get-env %s)\n__lm_builtin:unset-env %s\n",
			elvishQuote(pin.target), elvishQuote(pin.source), elvishQuote(pin.source))
	}
	script.WriteString("var __lm_callable = $nil\n")
	if len(prepared.candidates) == 0 {
		fmt.Fprintf(&script, "var __lm_program = %s\n", elvishQuote(spec.Launch.Program))
	} else {
		fmt.Fprintf(&script, "var __lm_program = ''\nfor candidate [%s] {\n", elvishArguments(prepared.candidates))
		script.WriteString("  if " + elvishProfileCallable("$candidate'~'") + " {\n" +
			"    set __lm_callable = $__lm_profile[$candidate'~']\n" +
			"    break\n  }\n" +
			"  if (__lm_builtin:has-external $candidate) {\n" +
			"    set __lm_program = (__lm_path:abs (__lm_builtin:search-external $candidate))\n" +
			"    break\n  }\n}\n")
		fmt.Fprintf(&script, "if (and (__lm_builtin:eq $__lm_program '') (__lm_builtin:eq $__lm_callable $nil)) {\n  %s >&2\n  __lm_builtin:exit 127\n}\n",
			strings.TrimSpace(d.protocolLine(prepared.exhaustedMarker, true)))
	}
	fmt.Fprintf(&script, "var __lm_args = [%s]\n", elvishArguments(spec.BaseArgs))
	if optional := spec.EnvGated; optional != nil {
		conditions := make([]string, len(optional.EnvVars))
		for i, name := range optional.EnvVars {
			quoted := elvishQuote(name)
			conditions[i] = "(and (__lm_builtin:has-env " + quoted + ") (not (__lm_builtin:eq (__lm_builtin:get-env " + quoted + ") '')))"
		}
		fmt.Fprintf(&script, "if (or %s) {\n%s} else {\n%sset __lm_args = [$@__lm_args %s]\n}\n",
			strings.Join(conditions, " "),
			d.protocolLine(prepared.MetaPrefix+optional.MetaKey+"=false", true),
			d.protocolLine(prepared.MetaPrefix+optional.MetaKey+"=true", true),
			elvishArguments(optional.Args))
	}
	if len(prepared.environmentProbe) == 0 {
		script.WriteString(d.protocolLine(prepared.Delimiter, spec.EnvGated == nil))
	} else {
		script.WriteString("var __lm_observed = $false\n")
		fmt.Fprintf(&script, "if (__lm_builtin:eq $__lm_callable $nil) { try { (__lm_builtin:external %s) %s; set __lm_observed = $true } catch _ { } }\n",
			elvishQuote(prepared.environmentProbe[0]), elvishArguments(prepared.environmentProbe[1:]))
		fmt.Fprintf(&script, "if $__lm_observed {\n%s} else {\n%s}\n",
			d.protocolLine(prepared.Delimiter, false), d.protocolLine(prepared.Delimiter, spec.EnvGated == nil))
	}
	// Elvish has no process-replacement builtin on Windows.
	invokeProgram := "(__lm_builtin:external $__lm_program) $@__lm_args"
	if runtime.GOOS != "windows" {
		invokeProgram = "__lm_builtin:exec $__lm_program $@__lm_args"
	}
	fmt.Fprintf(&script, `try {
  if (__lm_builtin:eq $__lm_callable $nil) {
    %s
  } else {
    $__lm_callable $@__lm_args
  }
} catch failure {
  if (__lm_builtin:has-key $failure[reason] exit-status) {
    __lm_builtin:exit $failure[reason][exit-status]
  }
  __lm_builtin:fail $failure[reason]
}
`, invokeProgram)
	return script.String(), nil
}

func elvishArguments(args []string) string {
	return strings.Join(quotedArguments(args, elvishQuote), " ")
}
