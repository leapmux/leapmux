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

func buildPosixCommand(shell string, spec shellLaunchSpec, prepared *PreparedLaunch) string {
	quote := shellquote.POSIX.Quote
	var script strings.Builder
	script.WriteString(posixProtocolSetup(shell))
	script.WriteString(posixLaunchEnvironment(shell, spec.StripEnvKeys, spec.pins, false))
	if len(prepared.candidates) == 0 {
		fmt.Fprintf(&script, "__lm_kind=external\n__lm_path=%s\n", quote(spec.Launch.Program))
	} else {
		script.WriteString(posixLookupSetup(shell))
		fmt.Fprintf(&script, "for __lm_selected in %s; do __leapmux_lookup \"$__lm_selected\" command; [ \"$__lm_kind\" = missing ] || break; done\n",
			strings.Join(quotedArguments(prepared.candidates, quote), " "))
		fmt.Fprintf(&script, "if [ \"$__lm_kind\" = missing ]; then\n__leapmux_print '\\n%%s\\n' %s >&2\n__leapmux_missing\nelse\n", quote(prepared.exhaustedMarker))
		script.WriteString(`case "$__lm_path" in /*|[A-Za-z]:/*) ;; *) __lm_path=./$__lm_path;; esac
`)
	}
	if runtime.GOOS == "windows" {
		script.WriteString(posixLaunchEnvironment(shell, nil, spec.pins, true))
	}
	script.WriteString("__leapmux_invoke() {\n")
	script.WriteString(protocolLine(shell, prepared.Delimiter, spec.EnvGated == nil))
	script.WriteString("case \"$__lm_kind\" in\n")
	switch shell {
	case "bash":
		script.WriteString(bashSelectionDispatch)
	case "zsh":
		script.WriteString(zshSelectionDispatch)
	default:
		script.WriteString(`external) \exec "$__lm_path" "$@";;
alias) \eval "$__lm_selected" '"$@"';;
`)
	}
	script.WriteString("callable) \"$__lm_selected\" \"$@\";;\nesac\n}\n")
	baseArgs := strings.Join(quotedArguments(spec.BaseArgs, quote), " ")
	if optional := spec.EnvGated; optional != nil {
		additionalArgs := strings.Join(quotedArguments(optional.Args, quote), " ")
		fmt.Fprintf(&script, "if %s; then\n%s__leapmux_invoke %s\nelse\n%s__leapmux_invoke %s %s\nfi\n",
			posixEnvCondition(optional.EnvVars), protocolLine(shell, prepared.MetaPrefix+optional.MetaKey+"=false", true), baseArgs,
			protocolLine(shell, prepared.MetaPrefix+optional.MetaKey+"=true", true), baseArgs, additionalArgs)
	} else {
		fmt.Fprintf(&script, "__leapmux_invoke %s\n", baseArgs)
	}
	if len(prepared.candidates) != 0 {
		script.WriteString("fi\n")
	}
	return script.String()
}

// Bash temporarily uses POSIX special-builtin precedence. Alias invocation
// restores the shell mode after parsing and before calling the user's wrapper.
const bashSelectionDispatch = `external)
	case ":$SHELLOPTS:" in
		*:posix:*) \exec "$__lm_path" "$@";;
		*) POSIXLY_CORRECT=y; \export -n POSIXLY_CORRECT; \exec "$__lm_path" "$@";;
	esac;;
alias)
	__lm_eval_prefix=''
	case ":$SHELLOPTS:" in
		*:posix:*) ;;
		*) __lm_eval_prefix='\set +o posix;';;
	esac
	POSIXLY_CORRECT=${POSIXLY_CORRECT-y}
	\eval "$__lm_eval_prefix" "$__lm_selected" '"$@"';;
`

const zshSelectionDispatch = `external)
	if (( $+functions[builtin] && !$+functions[unfunction] )); then
		\unfunction builtin
	fi
	if (( !$+functions[builtin] )); then
		\builtin exec \builtin \command "$__lm_path" "$@"
	else
		case "$__lm_path" in /*) ;; *) __lm_path="$PWD/$__lm_path";; esac
		while (( $+functions[$__lm_path] )); do
			__lm_path="${__lm_path%/*}/./${__lm_path##*/}"
		done
		if (( !$+functions[exec] )); then \exec "$__lm_path" "$@"; else "$__lm_path" "$@"; fi
	fi;;
alias)
	__lm_alias_argzero=$0
	functions[__leapmux_alias]='0=$__lm_alias_argzero; '"$__lm_selected"' "$@"'
	__leapmux_alias "$@";;
`

// Apply environment changes in the launch shell. A subshell would lose them
// when it returned. Keep profile functions available to the selected command.
func posixLaunchEnvironment(shell string, strip []string, pins []environmentPin, msys bool) string {
	if len(strip) == 0 && len(pins) == 0 && !msys {
		return ""
	}
	var script strings.Builder
	prefix := "\\"
	switch shell {
	case "bash":
		script.WriteString(`__lm_restore_mode=false
case ":$SHELLOPTS:" in
	*:posix:*) ;;
	*) POSIXLY_CORRECT=y; __lm_restore_mode=true;;
esac
`)
	case "zsh":
		prefix = "\\builtin \\"
		script.WriteString(`__lm_restore_builtin=false
if (( $+functions[builtin] )); then
	__lm_builtin_definition=$functions[builtin]
	if (( !$+functions[unfunction] )); then
		\unfunction builtin
	elif (( !$+functions[unset] )); then
		\unset -f builtin
	else
		\disable -f builtin
	fi
	__lm_restore_builtin=true
fi
`)
	}
	if len(strip) != 0 {
		fmt.Fprintf(&script, "%sunset %s || %sexit $?\n", prefix, strings.Join(strip, " "), prefix)
	}
	if !msys {
		for _, pin := range pins {
			fmt.Fprintf(&script, "%sexport %s=\"$%s\" || %sexit $?\n", prefix, pin.target, pin.source, prefix)
			fmt.Fprintf(&script, "%sunset %s || %sexit $?\n", prefix, pin.source, prefix)
		}
	} else {
		fmt.Fprintf(&script, "%sexport MSYS2_ARG_CONV_EXCL='*' || %sexit $?\n", prefix, prefix)
		for _, pin := range pins {
			if !strings.EqualFold(pin.target, "PATH") {
				fmt.Fprintf(&script, "%sexport MSYS2_ENV_CONV_EXCL=\"${MSYS2_ENV_CONV_EXCL-};%s=\" || %sexit $?\n", prefix, pin.target, prefix)
			}
		}
	}
	switch shell {
	case "bash":
		script.WriteString("if [ \"$__lm_restore_mode\" = true ]; then \\set +o posix; fi\n")
	case "zsh":
		script.WriteString("if [ \"$__lm_restore_builtin\" = true ]; then functions[builtin]=$__lm_builtin_definition; fi\n")
	}
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
			pwshEnvCondition(gate.EnvVars), protocolLine("pwsh", prepared.MetaPrefix+gate.MetaKey+"=false", true), protocolLine("pwsh", prepared.MetaPrefix+gate.MetaKey+"=true", true))
		writeArgs(gate.Args, true)
		script.WriteString("}; ")
	}
	script.WriteString(protocolLine("pwsh", prepared.Delimiter, spec.EnvGated == nil))
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

func buildNuSelection(spec shellLaunchSpec, prepared *PreparedLaunch, invocations map[string]nuInvocationFiles) string {
	var script strings.Builder
	script.WriteString(nuClearEnv(spec.StripEnvKeys) + nuSetEnv(spec.pins) + "\n")
	script.WriteString(nuLookupSetup)
	fmt.Fprintf(&script, "let __lm_selected = ([%s] | %%each {|name| let target = (leapmux-lookup $name false); if $target != null { {name: $name, target: $target} } } | %%take 1 | %%get -o 0)\n",
		strings.Join(quotedArguments(prepared.candidates, nuQuote), ", "))
	fmt.Fprintf(&script, "if $__lm_selected == null { %%print -e ''; %%print -e %s; %%exit 127 }\n", nuQuote(prepared.exhaustedMarker))
	script.WriteString(nuLaunchArguments(spec.WrapSpec, prepared.Delimiter, prepared.MetaPrefix))
	script.WriteString("if $__lm_selected.target.type == 'external' { ^$__lm_selected.target.path ...$__lm_args } else {\nmatch $__lm_selected.name {\n")
	for _, name := range prepared.candidates {
		files := invocations[name]
		fmt.Fprintf(&script, "%s => { ", nuQuote(name))
		if files.optional != "" {
			fmt.Fprintf(&script, "if ($__lm_args | %%length) == %d { run --full-reparse %s $__lm_args } else { run --full-reparse %s $__lm_args }", len(spec.BaseArgs), nuQuote(files.base), nuQuote(files.optional))
		} else {
			fmt.Fprintf(&script, "run --full-reparse %s $__lm_args", nuQuote(files.base))
		}
		script.WriteString(" }\n")
	}
	script.WriteString("}\n}\n")
	return script.String()
}

func nuLaunchArguments(spec WrapSpec, delimiter, metaPrefix string) string {
	var script strings.Builder
	fmt.Fprintf(&script, "let __lm_base = [%s]\n", strings.Join(quotedArguments(spec.BaseArgs, nuQuote), ", "))
	if gate := spec.EnvGated; gate != nil {
		fmt.Fprintf(&script, "let __lm_args = if (%s) { %s$__lm_base } else { %s$__lm_base ++ [%s] }\n",
			nuEnvCondition(gate.EnvVars), protocolLine("nu", metaPrefix+gate.MetaKey+"=false", true),
			protocolLine("nu", metaPrefix+gate.MetaKey+"=true", true), strings.Join(quotedArguments(gate.Args, nuQuote), ", "))
	} else {
		script.WriteString("let __lm_args = $__lm_base\n")
	}
	script.WriteString(protocolLine("nu", delimiter, spec.EnvGated == nil))
	return script.String()
}

// Csh substitutes variables before evaluating a line. Keep each existence
// check in cshEnvSeed on a separate line from the read it guards.
// cshQuote protects the program and arguments from history expansion and
// trailing backslashes even in branches the shell skips.
func buildCshCommand(spec shellLaunchSpec, prepared *PreparedLaunch) string {
	ordered := len(prepared.candidates) != 0
	var script strings.Builder
	script.WriteString(cshClearEnv(spec.StripEnvKeys) + cshSetEnv(spec.pins))
	if ordered {
		script.WriteString("\nset __lm_chosen = 0\nset __lm_status = 127\n")
	}
	baseArgs := strings.Join(quotedArguments(spec.BaseArgs, cshQuote), " ")
	optional := spec.EnvGated
	var argsWithOptional string
	if optional != nil {
		script.WriteString(cshEnvSeed(optional.EnvVars))
		argsWithOptional = baseArgs + " " + strings.Join(quotedArguments(optional.Args, cshQuote), " ")
	}
	writeInvocation := func(recordTerminator string, dispatch func(string) string) {
		writeRecord := func(record string, first bool) {
			script.WriteString(strings.TrimSuffix(protocolLine("csh", record, first), "\n"))
			script.WriteString(recordTerminator)
		}
		if optional == nil {
			writeRecord(prepared.Delimiter, true)
			script.WriteString(dispatch(baseArgs) + "\n")
			return
		}
		fmt.Fprintf(&script, "if ( $%s ) then\n", cshEnvGateVar)
		writeRecord(prepared.MetaPrefix+optional.MetaKey+"=false", true)
		writeRecord(prepared.Delimiter, false)
		script.WriteString(dispatch(baseArgs) + "\nelse\n")
		writeRecord(prepared.MetaPrefix+optional.MetaKey+"=true", true)
		writeRecord(prepared.Delimiter, false)
		script.WriteString(dispatch(argsWithOptional) + "\nendif\n")
	}
	if !ordered {
		writeInvocation(" && ", func(args string) string {
			return "exec " + cshQuote(spec.Launch.Program) + " " + args
		})
		return strings.TrimSuffix(script.String(), "\n")
	}
	for _, name := range prepared.candidates {
		script.WriteString("if ( ! $__lm_chosen ) then\n")
		script.WriteString(cshLookup(name, true))
		script.WriteString("if ( \"$__lm_kind\" != missing ) then\nset __lm_chosen = 1\n")
		// Save the command status before Tcsh leaves the conditional.
		writeInvocation("\n", func(arguments string) string {
			return fmt.Sprintf("if ( \"$__lm_kind\" == external ) then\nexec \"$__lm_path\" %s\nset __lm_status = $status\nelse\n%s %s\nset __lm_status = $status\nendif", arguments, name, arguments)
		})
		script.WriteString("endif\nendif\n")
	}
	fmt.Fprintf(&script, "if ( ! $__lm_chosen ) then\n'/bin/echo' '' > /dev/stderr\n'/bin/echo' %s > /dev/stderr\nendif\nunalias exit\nexit $__lm_status\n", cshQuote(prepared.exhaustedMarker))
	return script.String()
}
