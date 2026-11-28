package launch

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/util/shellquote"
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
	exhaust := "(__leapmux_builtin exit 127)"
	switch shell {
	case "bash":
		exhaust = "(POSIXLY_CORRECT=y; \\exit 127)"
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
		exhaust = `(if (( !$+functions[exit] )); then \exit 127; else __leapmux_builtin exit 127; fi)`
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
` + "__leapmux_print() { __leapmux_builtin printf \"$@\"; }\n" +
		"__leapmux_missing() " + exhaust + "\n"
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
	pathSetup := "__lm_rest=${PATH-}"
	bashHash := `if __lm_path=$(__leapmux_builtin hash -t -- "$1" 2>/dev/null) && __leapmux_path_usable "$2"; then return 0; fi
	`
	cachedPath := ""
	switch shell {
	case "bash":
		functionTest = `[ "$(__leapmux_builtin type -t "$1")" = function ]`
		cachedPath = bashHash
	case "zsh":
		aliasTest = `[[ -o aliases ]] && (( $+aliases[$1] || $+galiases[$1] ))`
		functionTest = `(( $+functions[$1] ))`
		cachedPath = `__lm_path=${commands[$1]-}
	if [ -n "$__lm_path" ] && __leapmux_path_usable "$2"; then return 0; fi
	`
		pathSetup = `[ "${PATH+x}" ] || return 1
	__lm_rest=$PATH`
	case "ksh":
		functionTest = `__leapmux_builtin typeset -f "$1" >/dev/null 2>&1`
		pathSetup = `__lm_rest=${PATH-$(__leapmux_builtin -p getconf PATH 2>/dev/null)}
	[ -n "$__lm_rest" ] || return 1`
	default:
		cachedPath = `if [ -n "${BASH_VERSION-}" ]; then ` + bashHash + `fi
	`
	}
	return `
__leapmux_path_usable() {
	` + executableTest + `
}

__leapmux_lookup_path() {
	` + cachedPath + pathSetup + `
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

// Tcsh has a which builtin. BSD csh has no equivalent that sees the live
// aliases and PATH together. Read the alias and then search the shell's path.
func cshLookup(name string, includeAlias bool) string {
	setup := "set __lm_name = " + cshQuote(name) + "\nset __lm_kind = missing\n"
	if includeAlias {
		setup += "if ( \"`alias $__lm_name`\" != '' ) set __lm_kind = alias\n"
	}
	return setup + `
set __lm_path = ''
if ( "$__lm_kind" == missing && $?path ) then
foreach __lm_directory ( $path:q )
if ( "$__lm_directory" == '' ) set __lm_directory = .
set __lm_path = "$__lm_directory/$__lm_name"
if ( -f "$__lm_path" && -x "$__lm_path" ) then
set __lm_kind = external
break
endif
end
endif
`
}

func cshNativeProbe(name, present, absent string) string {
	return cshLookup(name, false) + fmt.Sprintf(`if ( "$__lm_kind" == external ) then
if ( "$__lm_path" !~ /* ) set __lm_path = "$cwd/$__lm_path"
'/bin/echo' '%s:'"$__lm_path"
else
'/bin/echo' '%s'
endif
`, present, absent)
}

const nuLookupSetup = `
def leapmux-lookup [name: string, native_only: bool] {
	%which --all $name | %each {|result|
		let allowed = if $result.type == 'external' {
			not $native_only or $nu.os-info.name != 'windows' or $result.path =~ '(?i)\.(exe|com)$'
		} else {
			not $native_only and $result.type in ['alias', 'custom']
		}
		if $allowed { $result }
	} | %take 1 | %get -o 0
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

// cshUnaliasBuiltins is the first line of each csh command that the package
// builds. An alias replaces a builtin at the start of a line, and tcsh and csh
// let a profile alias each builtin that the commands call there: set, setenv,
// unsetenv, exec, else, foreach and break. The line removes those aliases. It
// stands alone, because csh substitutes the aliases of a whole line before it
// runs any command of that line. tcsh refuses an alias named unalias.
//
// It removes no other alias, so a selected wrapper alias keeps the echo,
// which, eval and source of its profile. cshDialect.buildCommand removes the
// alias of exit only before its own exit, for the same reason. The other words
// that the commands use, such as then, endif and end, take no alias.
//
// No line of the file can remove an alias named if: it breaks the line that
// sources the file (see cshInvocationPrefix) before the file runs.
const cshUnaliasBuiltins = "unalias set setenv unsetenv exec else foreach break\n"

// protocolLine emits a complete record. The first record needs a separator
// because a profile can print a banner without a trailing newline.
func (posixDialect) protocolLine(line string, first bool) string {
	format := "%s\\n"
	if first {
		format = "\\n" + format
	}
	return "__leapmux_print '" + format + "' " + shellquote.POSIX.Quote(line) + " || exit $?\n"
}

func (d posixDialect) renderProbe(nonce string, names []string, nativeOnly, _ bool) string {
	setup := posixProtocolSetup(d.shell) + posixLookupSetup(d.shell)
	if d.shell == "bash" && !nativeOnly {
		var script strings.Builder
		script.WriteString(setup)
		fmt.Fprintf(&script, "__lm_probe_records=('' '%s:start')\n", nonce)
		for index, name := range names {
			fmt.Fprintf(&script, "__leapmux_lookup %s command\n", shellquote.POSIX.Quote(name))
			fmt.Fprintf(&script, "if [ \"$__lm_kind\" = missing ]; then __lm_probe_records+=('%s:%d:no'); else __lm_probe_records+=('%s:%d:yes'); fi\n", nonce, index, nonce, index)
		}
		fmt.Fprintf(&script, "__leapmux_print '%%s\\n' \"${__lm_probe_records[@]}\" '%s:complete'\n", nonce)
		return script.String()
	}
	return renderStandardProbe(setup, d, d.probeResult, nonce, names, nativeOnly)
}

func (d posixDialect) probeResult(name, present, absent string, nativeOnly bool) string {
	mode := "command"
	if nativeOnly {
		mode = "path"
	}
	setup := "__leapmux_lookup " + shellquote.POSIX.Quote(name) + " " + mode + "\n"
	if !nativeOnly {
		return setup + fmt.Sprintf("if [ \"$__lm_kind\" != missing ]; then __leapmux_print '%%s\\n' '%s'; else __leapmux_print '%%s\\n' '%s'; fi\n", present, absent)
	}
	directory := "$(__leapmux_builtin pwd -P)"
	if d.shell == "zsh" {
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

func (pwshDialect) protocolLine(line string, first bool) string {
	record := "[Console]::Out.WriteLine(" + shellquote.PowerShell.Quote(line) + ")\n"
	if first {
		return "[Console]::Out.WriteLine(''); " + record
	}
	return record
}

func (d pwshDialect) renderProbe(nonce string, names []string, nativeOnly, _ bool) string {
	setup := pwshLookupSetup
	if nativeOnly {
		setup += pwshNativeLookupSetup()
	}
	return renderStandardProbe(setup, d, d.probeResult, nonce, names, nativeOnly)
}

func (pwshDialect) probeResult(name, present, absent string, nativeOnly bool) string {
	if nativeOnly {
		return fmt.Sprintf("$path = __leapmux_native %s; if ($null -ne $path) { [Console]::Out.WriteLine('%s:' + $path) } else { [Console]::Out.WriteLine('%s') }\n",
			shellquote.PowerShell.Quote(name), present, absent)
	}
	return fmt.Sprintf("if ($null -ne (__leapmux_lookup %s)) { [Console]::Out.WriteLine('%s') } else { [Console]::Out.WriteLine('%s') }\n",
		shellquote.PowerShell.Quote(name), present, absent)
}

func (nuDialect) protocolLine(line string, first bool) string {
	record := "%print " + nuQuote(line) + "\n"
	if first {
		return "%print ''; " + record
	}
	return record
}

func (d nuDialect) renderProbe(nonce string, names []string, nativeOnly, _ bool) string {
	return renderStandardProbe(nuLookupSetup, d, d.probeResult, nonce, names, nativeOnly)
}

func (nuDialect) probeResult(name, present, absent string, nativeOnly bool) string {
	value := nuQuote(present)
	if nativeOnly {
		value = "(" + nuQuote(present+":") + " + ($__lm_result.path | %path expand))"
	}
	return fmt.Sprintf("let __lm_result = (leapmux-lookup %s %t); if $__lm_result != null { %%print %s } else { %%print '%s' }\n",
		nuQuote(name), nativeOnly, value, absent)
}

func (cshDialect) protocolLine(line string, first bool) string {
	record := "'/bin/echo' " + cshQuote(line) + "\n"
	if first {
		return "'/bin/echo' ''\n" + record
	}
	return record
}

func (d cshDialect) renderProbe(nonce string, names []string, nativeOnly, _ bool) string {
	return renderStandardProbe(cshUnaliasBuiltins, d, d.probeResult, nonce, names, nativeOnly)
}

func (cshDialect) probeResult(name, present, absent string, nativeOnly bool) string {
	if nativeOnly {
		return cshNativeProbe(name, present, absent)
	}
	return cshLookup(name, true) + fmt.Sprintf("if ( \"$__lm_kind\" != missing ) then\n'/bin/echo' '%s'\nelse\n'/bin/echo' '%s'\nendif\n", present, absent)
}
