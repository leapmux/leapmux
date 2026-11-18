package launch

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/util/shellquote"
)

func quotedArguments(args []string, quote func(string) string) []string {
	result := make([]string, len(args))
	for index, arg := range args {
		result[index] = quote(arg)
	}
	return result
}

func buildPosixSelection(shell string, spec shellLaunchSpec, prepared *PreparedLaunch) string {
	quote := shellquote.POSIX.Quote
	print := posixBuiltin(shell, "printf")
	var script strings.Builder
	script.WriteString(posixLookupSetup(shell))
	if prefix := posixClearEnv(spec.StripEnvKeys) + posixSetEnv(spec.pins); prefix != "" {
		script.WriteString(prefix + ":\n")
	}
	fmt.Fprintf(&script, "for __lm_selected in %s; do __leapmux_lookup \"$__lm_selected\" command; [ \"$__lm_kind\" = missing ] || break; done\n",
		strings.Join(quotedArguments(prepared.candidates, quote), " "))
	fmt.Fprintf(&script, "if [ \"$__lm_kind\" = missing ]; then %s '\\n%%s\\n' %s >&2; exit 127; fi\n", print, quote(prepared.exhaustedMarker))
	fmt.Fprintf(&script, "set -- %s\n", strings.Join(quotedArguments(spec.BaseArgs, quote), " "))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "if %s; then %s '\\n%%s\\n' %s; else %s '\\n%%s\\n' %s; set -- \"$@\" %s; fi\n",
			posixEnvCondition(gate.EnvVars), print, quote(prepared.MetaPrefix+gate.MetaKey+"=false"),
			print, quote(prepared.MetaPrefix+gate.MetaKey+"=true"), strings.Join(quotedArguments(gate.Args, quote), " "))
	}
	script.WriteString(probeLine(shell, prepared.Delimiter))
	if runtime.GOOS == "windows" {
		script.WriteString("export MSYS2_ARG_CONV_EXCL='*'\n")
		for _, entry := range spec.SetEnv {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, "PATH") {
				fmt.Fprintf(&script, "export MSYS2_ENV_CONV_EXCL=\"${MSYS2_ENV_CONV_EXCL-};%s=\"\n", key)
			}
		}
	}
	fmt.Fprintf(&script, "case \"$__lm_kind\" in\nexternal) %s \"$__lm_path\" \"$@\";;\n", posixBuiltin(shell, "exec"))
	script.WriteString("alias) ")
	if shell == "bash" {
		script.WriteString("builtin shopt -s expand_aliases; ")
	}
	script.WriteString(posixBuiltin(shell, "eval") + " \"$__lm_selected\" '\"$@\"';;\ncallable) \"$__lm_selected\" \"$@\";;\nesac\n")
	return script.String()
}

func buildFishCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	script.WriteString(fishClearEnv(spec.StripEnvKeys) + fishSetEnv(spec.pins))
	if len(prepared.candidates) != 0 {
		script.WriteString(fishLookupSetup)
		fmt.Fprintf(&script, "set -l __lm_selected\nfor __lm_candidate in %s\nset __lm_selected \"$__lm_candidate\"\n__leapmux_lookup \"$__lm_selected\" command\nif test \"$__lm_kind\" != missing; break; end\nend\n", strings.Join(quotedArguments(prepared.candidates, fishQuote), " "))
		fmt.Fprintf(&script, "if test \"$__lm_kind\" = missing; builtin printf '\\n%%s\\n' %s >&2; exit 127; end\n", fishQuote(prepared.exhaustedMarker))
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
			strings.Join(conditions, "; or "), probeLine("fish", prepared.MetaPrefix+gate.MetaKey+"=false"),
			probeLine("fish", prepared.MetaPrefix+gate.MetaKey+"=true"), strings.Join(quotedArguments(gate.Args, fishQuote), " "))
	}
	script.WriteString(probeLine("fish", prepared.Delimiter))
	script.WriteString("if test \"$__lm_kind\" = external; exec \"$__lm_path\" $__lm_args; else; $__lm_selected $__lm_args; end\nexit $status\n")
	return script.String()
}

func buildPwshCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	quote := shellquote.PowerShell.Quote
	var script strings.Builder
	script.WriteString(pwshLookupSetup)
	script.WriteString("$ErrorActionPreference = 'Stop'; ")
	script.WriteString(pwshClearEnv(spec.StripEnvKeys) + pwshSetEnv(spec.pins))
	if len(prepared.candidates) == 0 {
		fmt.Fprintf(&script, "$__lm_selected = __leapmux_lookup %s; if ($null -eq $__lm_selected) { exit 127 }; ", quote(spec.Launch.Program))
	} else {
		fmt.Fprintf(&script, "$__lm_selected = $null; foreach ($__lm_candidate in @(%s)) { $__lm_selected = __leapmux_lookup $__lm_candidate; if ($null -ne $__lm_selected) { break } }; ",
			strings.Join(quotedArguments(prepared.candidates, quote), ", "))
		fmt.Fprintf(&script, "if ($null -eq $__lm_selected) { [Console]::Error.WriteLine(''); [Console]::Error.WriteLine(%s); exit 127 }; ", quote(prepared.exhaustedMarker))
	}
	writeArgs := func(args []string, appendArgs bool) {
		native := make([]string, len(args))
		for index, arg := range args {
			native[index] = quote(legacyNativeArgument(arg))
		}
		assignment := "="
		if appendArgs {
			assignment = "+="
		}
		fmt.Fprintf(&script, "if ($__lm_selected.Native) { $__lm_args %s @(%s) } else { $__lm_args %s @(%s) }; ",
			assignment, strings.Join(native, ", "), assignment, strings.Join(quotedArguments(args, quote), ", "))
	}
	writeArgs(spec.BaseArgs, false)
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "if (%s) { %s} else { %s",
			pwshEnvCondition(gate.EnvVars), probeLine("pwsh", prepared.MetaPrefix+gate.MetaKey+"=false"), probeLine("pwsh", prepared.MetaPrefix+gate.MetaKey+"=true"))
		writeArgs(gate.Args, true)
		script.WriteString("}; ")
	}
	script.WriteString(probeLine("pwsh", prepared.Delimiter))
	script.WriteString(`$global:LASTEXITCODE = $null
$PSNativeCommandUseErrorActionPreference = $false
if ($__lm_selected.Native) { $PSNativeCommandArgumentPassing = 'Legacy' }
& $__lm_selected.Token @__lm_args
$__lm_success = $?
if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }
if ($__lm_success) { exit 0 }
exit 1
`)
	return script.String()
}

func buildNuSelection(spec shellLaunchSpec, prepared *PreparedLaunch, invocations map[string]string) string {
	var script strings.Builder
	script.WriteString(nuClearEnv(spec.StripEnvKeys) + nuSetEnv(spec.pins) + "\n")
	script.WriteString(nuLookupSetup)
	fmt.Fprintf(&script, "let __lm_selected = ([%s] | each {|name| let target = (leapmux-lookup $name $__lm_deferred); if $target != null { {name: $name, target: $target} } } | get -o 0)\n",
		strings.Join(quotedArguments(prepared.candidates, nuQuote), ", "))
	fmt.Fprintf(&script, "if $__lm_selected == null { print -e ''; print -e %s; exit 127 }\n", nuQuote(prepared.exhaustedMarker))
	fmt.Fprintf(&script, "let __lm_base = [%s]\n", strings.Join(quotedArguments(spec.BaseArgs, nuQuote), ", "))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "let __lm_args = if (%s) { %s$__lm_base } else { %s$__lm_base ++ [%s] }\n",
			nuEnvCondition(gate.EnvVars), probeLine("nu", prepared.MetaPrefix+gate.MetaKey+"=false"),
			probeLine("nu", prepared.MetaPrefix+gate.MetaKey+"=true"), strings.Join(quotedArguments(gate.Args, nuQuote), ", "))
	} else {
		script.WriteString("let __lm_args = $__lm_base\n")
	}
	script.WriteString(probeLine("nu", prepared.Delimiter))
	script.WriteString("if $__lm_selected.target.type == 'external' { ^$__lm_selected.target.path ...$__lm_args } else {\nmatch $__lm_selected.name {\n")
	for _, name := range prepared.candidates {
		fmt.Fprintf(&script, "%s => { run --full-reparse %s $__lm_args }\n", nuQuote(name), nuQuote(invocations[name]))
	}
	script.WriteString("}\n}\n")
	return script.String()
}

func buildCshSelection(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	var script strings.Builder
	script.WriteString(lookupSetup("csh"))
	script.WriteString(cshClearEnv(spec.StripEnvKeys) + cshSetEnv(spec.pins) + "\n")
	script.WriteString("set __lm_chosen = 0\nset __lm_status = 127\n")
	args := strings.Join(quotedArguments(spec.BaseArgs, cshQuote), " ")
	if gate := spec.EnvGated; gate != nil {
		script.WriteString(cshEnvSeed(gate.EnvVars))
	}
	for _, name := range prepared.candidates {
		script.WriteString("if ( ! $__lm_chosen ) then\n")
		script.WriteString(cshLookup(name, false))
		script.WriteString("if ( \"$__lm_kind\" != missing ) then\nset __lm_chosen = 1\n")
		// Save the command status before Tcsh leaves the conditional.
		invoke := func(arguments string) {
			script.WriteString(probeLine("csh", prepared.Delimiter))
			fmt.Fprintf(&script, "if ( \"$__lm_kind\" == external ) then\nexec \"$__lm_path\" %s\nset __lm_status = $status\nelse\n%s %s\nset __lm_status = $status\nendif\n", arguments, name, arguments)
		}
		if gate := spec.EnvGated; gate != nil {
			fmt.Fprintf(&script, "if ( $%s ) then\n", cshEnvGateVar)
			script.WriteString(probeLine("csh", prepared.MetaPrefix+gate.MetaKey+"=false"))
			invoke(args)
			script.WriteString("else\n")
			script.WriteString(probeLine("csh", prepared.MetaPrefix+gate.MetaKey+"=true"))
			invoke(args + " " + strings.Join(quotedArguments(gate.Args, cshQuote), " "))
			script.WriteString("endif\n")
		} else {
			invoke(args)
		}
		script.WriteString("endif\nendif\n")
	}
	fmt.Fprintf(&script, "if ( ! $__lm_chosen ) then\necho '' > /dev/stderr\necho %s > /dev/stderr\nendif\nexit $__lm_status\n", cshQuote(prepared.exhaustedMarker))
	return script.String()
}
