package launch

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// A provider command is a single bare token even in shells that require a
// literal command word to expand aliases. Paths use the direct program form.
var providerCommandName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func posixProtocolSetup(shell string) string {
	// Remove only the function that shadows the builtin dispatcher. The
	// subshell leaves the original functions available to the provider.
	// Bash's POSIX mode gives the unset builtin precedence over functions.
	dispatch := `\unset -f command
	\command "$@"`
	switch shell {
	case "bash":
		dispatch = `POSIXLY_CORRECT=y
	if \unset -f builtin 2>/dev/null; then
		\builtin "$@"
	elif \unset -f command 2>/dev/null; then
		\command "$@"
	elif \unset -f "$1" 2>/dev/null; then
		"$@"
	else
		` + posixFormatterFallback(shell) + `
	fi`
	case "zsh":
		dispatch = `if (( !$+functions[builtin] )); then
		\builtin "$@"
	elif (( !$+functions[unfunction] )); then
		\unfunction builtin && \builtin "$@"
	elif (( !$+functions[unset] )); then
		\unset -f builtin && \builtin "$@"
	else
		` + posixFormatterFallback(shell) + `
	fi`
	}
	return `__leapmux_builtin() (
	` + dispatch + `
)
` + "__leapmux_print() { __leapmux_builtin printf \"$@\"; }\n"
}

// The shell can run its own formatter in the common case. Fall back to a
// system executable if the profile also shadows the native dispatchers.
func posixFormatterFallback(shell string) string {
	formatter := ""
	if runtime.GOOS == "windows" {
		formatter = "/usr/bin/printf"
	} else {
		for _, program := range []string{"/usr/bin/printf", "/bin/printf", "printf"} {
			if path, err := exec.LookPath(program); err == nil && filepath.IsAbs(path) {
				formatter = path
				break
			}
		}
	}
	if formatter == "" {
		return "return 1"
	}
	quoted := shellquote.POSIX.Quote(formatter)
	if shell == "zsh" {
		// Zsh allows an absolute path as a function name. Use an equivalent
		// spelling that the profile has not defined as a function.
		return `case "$1" in
			printf)
				__lm_formatter=` + quoted + `
				while (( $+functions[$__lm_formatter] )); do
					__lm_formatter="${__lm_formatter%/*}/./${__lm_formatter##*/}"
				done
				"$__lm_formatter" "${@[2,-1]}";;
			*) return 1;;
		esac`
	}
	return `case "$1" in
		printf) \exec ` + quoted + ` "${@:2}";;
		*) return 1;;
	esac`
}

func posixLookupSetup(shell string) string {
	executableTest := `[ -f "$__lm_path" ] && [ -x "$__lm_path" ]`
	windowsExecutables := ""
	if runtime.GOOS == "windows" {
		executableTest = `[ -f "$__lm_path" ] && [ -x "$__lm_path" ] || return 1
	[ "$1" = command ] && return 0
	case "$__lm_path" in
		*.[eE][xX][eE]|*.[cC][oO][mM]) return 0;;
		*) return 1;;
	esac`
		windowsExecutables = `for __lm_ext in .exe .com; do
			if [ -f "$__lm_path$__lm_ext" ] && [ -x "$__lm_path$__lm_ext" ]; then
				__lm_path=$__lm_path$__lm_ext
				return 0
			fi
		done
		`
	}
	// POSIX command -v reports a function by its bare name. Unsetting that
	// function in a subshell distinguishes it from an ordinary builtin.
	functionTest := `[ "$(__leapmux_builtin -v "$1" 2>/dev/null)" = "$1" ] &&
			[ "$(\unset -f command; \unset -f "$1" 2>/dev/null; \command -v "$1" 2>/dev/null)" != "$1" ]`
	aliasTest := `__leapmux_builtin alias "$1" >/dev/null 2>&1`
	defaultPath := "__leapmux_builtin -p getconf PATH"
	cachedPath := ""
	switch shell {
	case "bash":
		functionTest = `[ "$(__leapmux_builtin type -t "$1")" = function ]`
		cachedPath = `if __lm_path=$(__leapmux_builtin hash -t -- "$1" 2>/dev/null) && __leapmux_path_usable "$2"; then return 0; fi
	`
		defaultPath = "__leapmux_builtin command -p getconf PATH"
	case "zsh":
		aliasTest = `(( $+aliases[$1] || $+galiases[$1] ))`
		functionTest = `(( $+functions[$1] ))`
		defaultPath = "\\command -p getconf PATH"
	case "ksh":
		functionTest = `__leapmux_builtin typeset -f "$1" >/dev/null 2>&1`
	}
	return `
__leapmux_path_usable() {
	` + executableTest + `
}

__leapmux_lookup_path() {
	` + cachedPath + `__lm_rest=${PATH-$(` + defaultPath + ` 2>/dev/null)}
	while :; do
		__lm_dir=${__lm_rest%%:*}
		__lm_path=${__lm_dir:-.}/$1
		if __leapmux_path_usable "$2"; then return 0; fi
		` + windowsExecutables + `case "$__lm_rest" in
			*:*) __lm_rest=${__lm_rest#*:};;
			*) return 1;;
		esac
	done
}

__leapmux_lookup() {
	__lm_kind=missing
	__lm_path=''
	if [ "$2" = command ]; then
		if ` + aliasTest + `; then __lm_kind=alias; return; fi
		if ` + functionTest + `; then __lm_kind=callable; return; fi
	fi
	if __leapmux_lookup_path "$1" "$2"; then __lm_kind=external; fi
}
`
}

const fishLookupSetup = `
function __leapmux_lookup
	set -g __lm_kind missing
	set -g __lm_path ''
	if test "$argv[2]" = command; and functions -q -- "$argv[1]"
		set -g __lm_kind callable
	else if set -l found (command -s -- "$argv[1]")
		if test -f "$found"; and test -x "$found"
			set -g __lm_kind external
			set -g __lm_path "$found"
		end
	end
end
`

func fishQuote(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(value) + "'"
}

func cshQuote(value string) string {
	var quoted strings.Builder
	parts := strings.Split(value, "'")
	for index, part := range parts {
		if index != 0 {
			quoted.WriteString(`\'`)
		}
		prefix := strings.TrimRight(part, `\`)
		quoted.WriteByte('\'')
		quoted.WriteString(strings.NewReplacer("!", `\!`, "\n", "\\\n").Replace(prefix))
		quoted.WriteByte('\'')
		// Csh skips conditional blocks by scanning their quotes. A trailing
		// backslash escapes the quote in that scan, even inside a literal.
		quoted.WriteString(strings.Repeat(`\\`, len(part)-len(prefix)))
	}
	return quoted.String()
}

func cshLookupSetup(nativeOnly bool) string {
	mode := "command"
	if nativeOnly {
		mode = "path"
	}
	// Csh has no functions. A private alias shares the lookup and keeps its
	// result in the launch shell. The single-line if defers the source call
	// until the child removes the source and which aliases.
	body := "set __lm_kind = missing; set __lm_path = ''; set __lm_alias = '';" +
		" if ( \"$__lm_lookup_mode\" == command ) set __lm_alias = \"`alias $__lm_name`\";" +
		" if ( \"$__lm_alias\" != '' ) set __lm_kind = alias;" +
		" if ( \"$__lm_kind\" == missing ) set __lm_path = \"`/usr/bin/printf 'which %s\\n' \"$__lm_name\" | ( unalias which; unalias $__lm_name; unalias source; if ( 1 ) source /dev/stdin )`\";" +
		" if ( \"$__lm_kind\" == missing && -f \"$__lm_path\" && -x \"$__lm_path\" ) set __lm_kind = external"
	return "set __lm_lookup_mode = " + mode + "\nalias __leapmux_lookup " + cshQuote(body) + "\n"
}

func cshLookup(name string) string {
	return "set __lm_name = " + cshQuote(name) + "\n__leapmux_lookup\n"
}

const nuLookupSetup = `
let __lm_version = (version | get version | split row '.')
let __lm_deferred = (($__lm_version.0 | into int) > 0 or ($__lm_version.1 | into int) >= 114)
def leapmux-lookup [name: string, allow_callable: bool, native_only: bool] {
	which --all $name | where {|result|
		if $result.type == 'external' {
			not $native_only or $nu.os-info.name != 'windows' or $result.path =~ '(?i)\.(exe|com)$'
		} else {
			$allow_callable and $result.type in ['alias', 'custom']
		}
	} | get -o 0
}
`

// Keep both the resolved command and the invocation token. For a qualified
// path, filesystem checks must be literal. Metadata lookup treats brackets as
// wildcards and can confuse a file path with a module-qualified command.
const pwshLookupSetup = `
function __leapmux_lookup($target) {
	$seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
	while ($seen.Add($target)) {
		$loaded = @($ExecutionContext.InvokeCommand.GetCommands($target, ([System.Management.Automation.CommandTypes]::Alias -bor [System.Management.Automation.CommandTypes]::Function -bor [System.Management.Automation.CommandTypes]::Filter -bor [System.Management.Automation.CommandTypes]::Cmdlet), $false))
		if ($loaded.Count -gt 0) {
			$resolved = $loaded[0]
		} else {
			$literal = $null
			if ([System.IO.Path]::IsPathRooted($target)) {
				$literal = $target
			} elseif ($target.Contains('/') -or $target.IndexOf([char]0x5c) -ge 0) {
				$literal = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($target)
			}
			if ($null -ne $literal) {
				if ([System.IO.File]::Exists($literal)) {
					return @{ Token = $literal; Native = [System.IO.Path]::GetExtension($literal) -ine '.ps1' }
				}
				if ([System.IO.File]::Exists($literal + '.ps1')) {
					return @{ Token = $literal + '.ps1'; Native = $false }
				}
			}
			$resolved = $ExecutionContext.InvokeCommand.GetCommand($target, [System.Management.Automation.CommandTypes]::All)
		}
		if ($null -eq $resolved) { return }
		if ($resolved -is [System.Management.Automation.AliasInfo]) {
			$target = $resolved.Definition
			continue
		}
		return @{ Token = $resolved; Native = $resolved.CommandType -eq [System.Management.Automation.CommandTypes]::Application }
	}
}
`

func pwshNativeLookupSetup() string {
	filter := "if ($true)"
	if runtime.GOOS == "windows" {
		filter = "if ([System.IO.Path]::GetExtension($application.Path) -in @('.exe', '.com'))"
	}
	return `function __leapmux_native($name) {
	foreach ($application in $ExecutionContext.InvokeCommand.GetCommands($name, [System.Management.Automation.CommandTypes]::Application, $false)) {
		` + filter + ` { return $application.Path }
	}
}
`
}

func isCsh(shell string) bool { return shell == "csh" || shell == "tcsh" }

func lookupSetup(shell string, nativeOnly bool) string {
	switch {
	case terminal.IsPwsh(shell):
		return pwshLookupSetup
	case shell == "nu":
		return nuLookupSetup
	case shell == "fish":
		return fishLookupSetup
	case shell == "xonsh":
		return xonshLookupSetup
	case isCsh(shell):
		return cshLookupSetup(nativeOnly)
	default:
		return posixProtocolSetup(shell) + posixLookupSetup(shell)
	}
}

// renderCommandProbe runs each lookup in the same profile and working directory.
// Metadata is line-framed separately from the profile's output.
func renderCommandProbe(shell, nonce string, names []string, nativeOnly bool) string {
	var script strings.Builder
	script.WriteString(lookupSetup(shell, nativeOnly))
	if nativeOnly && terminal.IsPwsh(shell) {
		script.WriteString(pwshNativeLookupSetup())
	}
	script.WriteString(protocolLine(shell, nonce+":start", true))
	for index, name := range names {
		prefix := nonce + ":" + strconv.Itoa(index) + ":"
		script.WriteString(commandProbeResult(shell, name, prefix, nativeOnly))
	}
	script.WriteString(protocolLine(shell, nonce+":complete", false))
	return script.String()
}

// protocolLine emits a complete record. The first record needs a separator
// because a profile can print a banner without a trailing newline.
func protocolLine(shell, line string, first bool) string {
	var separator, record string
	switch {
	case terminal.IsPwsh(shell):
		separator = "[Console]::Out.WriteLine(''); "
		record = "[Console]::Out.WriteLine(" + shellquote.PowerShell.Quote(line) + ")\n"
	case shell == "nu":
		separator = "%print ''; "
		record = "%print " + nuQuote(line) + "\n"
	case shell == "xonsh":
		if first {
			line = "\n" + line
		}
		return "__leapmux_write(__lm_sys.stdout, " + pythonString(line) + ")\n"
	case shell == "fish":
		separator = "builtin printf '\\n'; "
		record = "builtin printf '%s\\n' " + fishQuote(line) + "\n"
	case isCsh(shell):
		separator = "'/bin/echo' ''\n"
		record = "'/bin/echo' " + cshQuote(line) + "\n"
	default:
		format := "%s\\n"
		if first {
			format = "\\n" + format
		}
		return "__leapmux_print '" + format + "' " + shellquote.POSIX.Quote(line) + " || exit $?\n"
	}
	if first {
		return separator + record
	}
	return record
}

func commandProbeResult(shell, name, prefix string, nativeOnly bool) string {
	mode := "command"
	if nativeOnly {
		mode = "path"
	}
	present, absent := prefix+"yes", prefix+"no"
	quoted := shellquote.POSIX.Quote(name)
	switch {
	case terminal.IsPwsh(shell):
		if nativeOnly {
			return fmt.Sprintf("$path = __leapmux_native %s; if ($null -ne $path) { [Console]::Out.WriteLine('%s:' + $path) } else { [Console]::Out.WriteLine('%s') }\n",
				shellquote.PowerShell.Quote(name), present, absent)
		}
		return fmt.Sprintf("if ($null -ne (__leapmux_lookup %s)) { [Console]::Out.WriteLine('%s') } else { [Console]::Out.WriteLine('%s') }\n",
			shellquote.PowerShell.Quote(name), present, absent)
	case shell == "nu":
		allow, value := "$__lm_deferred", nuQuote(present)
		if nativeOnly {
			allow = "false"
			value = "(" + nuQuote(present+":") + " + ($__lm_result.path | path expand))"
		}
		return fmt.Sprintf("let __lm_result = (leapmux-lookup %s %s %t); if $__lm_result != null { %%print %s } else { %%print '%s' }\n",
			nuQuote(name), allow, nativeOnly, value, absent)
	case shell == "xonsh":
		return xonshProbeResult(name, present, absent, nativeOnly)
	case shell == "fish":
		if nativeOnly {
			return fmt.Sprintf("__leapmux_lookup %s path\nif test \"$__lm_kind\" = external; if not string match -q '/*' -- \"$__lm_path\"; set __lm_path \"$PWD/$__lm_path\"; end; builtin printf '%%s:%%s\\n' '%s' \"$__lm_path\"; else; builtin printf '%%s\\n' '%s'; end\n",
				fishQuote(name), present, absent)
		}
		return fmt.Sprintf("__leapmux_lookup %s command\nif test \"$__lm_kind\" != missing; builtin printf '%%s\\n' '%s'; else; builtin printf '%%s\\n' '%s'; end\n",
			fishQuote(name), present, absent)
	case isCsh(shell):
		result := cshLookup(name)
		if nativeOnly {
			return result + fmt.Sprintf("if ( \"$__lm_kind\" == external ) then\nif ( \"$__lm_path\" !~ /* ) set __lm_path = \"$cwd/$__lm_path\"\n'/bin/echo' '%s:'\"$__lm_path\"\nelse\n'/bin/echo' '%s'\nendif\n", present, absent)
		}
		return result + fmt.Sprintf("if ( \"$__lm_kind\" != missing ) then\n'/bin/echo' '%s'\nelse\n'/bin/echo' '%s'\nendif\n", present, absent)
	default:
		setup := "__leapmux_lookup " + quoted + " " + mode + "\n"
		if !nativeOnly {
			return setup + fmt.Sprintf("if [ \"$__lm_kind\" != missing ]; then __leapmux_print '%%s\\n' '%s'; else __leapmux_print '%%s\\n' '%s'; fi\n", present, absent)
		}
		directory := "$(__leapmux_builtin pwd -P)"
		if shell == "zsh" {
			directory = "$PWD"
		}
		normalize := `case "$__lm_path" in /*) ;; *) __lm_path="` + directory + `/$__lm_path";; esac` + "\n"
		if runtime.GOOS == "windows" {
			normalize += `if [ -x /usr/bin/cygpath ]; then __lm_path=$(/usr/bin/cygpath -w -- "$__lm_path") || exit 1; fi
`
		}
		return setup + "if [ \"$__lm_kind\" = external ]; then\n" + normalize +
			fmt.Sprintf("__leapmux_print '%%s:%%s\\n' '%s' \"$__lm_path\"\nelse\n__leapmux_print '%%s\\n' '%s'\nfi\n", present, absent)
	}
}
