package launch

import (
	"fmt"
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

const posixPathLookup = `
__leapmux_lookup_path() {
	__lm_rest=${PATH-$(\command -p getconf PATH 2>/dev/null)}
	while :; do
		__lm_dir=${__lm_rest%%:*}
		__lm_path=${__lm_dir:-.}/$1
		if [ -f "$__lm_path" ] && [ -x "$__lm_path" ]; then return 0; fi
		%WINDOWS_EXECUTABLES%
		case "$__lm_rest" in
			*:*) __lm_rest=${__lm_rest#*:};;
			*) return 1;;
		esac
	done
}
`

func posixBuiltin(shell, name string) string {
	if shell == "bash" || shell == "zsh" {
		return `\builtin ` + name
	}
	return `\command ` + name
}

func posixLookupSetup(shell string) string {
	windowsExecutables := ""
	if runtime.GOOS == "windows" {
		windowsExecutables = `for __lm_ext in .exe .com; do
			if [ -f "$__lm_path$__lm_ext" ] && [ -x "$__lm_path$__lm_ext" ]; then
				__lm_path=$__lm_path$__lm_ext
				return 0
			fi
		done`
	}
	// POSIX command -v reports a function by its bare name. Unsetting that
	// function in a subshell distinguishes it from an ordinary builtin.
	functionTest := `[ "$(\command -v "$1" 2>/dev/null)" = "$1" ] &&
			[ "$(\command unset -f "$1" 2>/dev/null; \command -v "$1" 2>/dev/null)" != "$1" ]`
	switch shell {
	case "bash":
		functionTest = `[ "$(\builtin type -t "$1")" = function ]`
	case "zsh":
		functionTest = `[ "$(\builtin whence -w "$1")" = "$1: function" ]`
	case "ksh":
		functionTest = `\command typeset -f "$1" >/dev/null 2>&1`
	}
	return strings.Replace(posixPathLookup, "%WINDOWS_EXECUTABLES%", windowsExecutables, 1) + `
__leapmux_lookup() {
	__lm_kind=missing
	__lm_path=''
	if [ "$2" = command ]; then
		if ` + posixBuiltin(shell, "alias") + ` "$1" >/dev/null 2>&1; then __lm_kind=alias; return; fi
		if ` + functionTest + `; then __lm_kind=callable; return; fi
	fi
	if __leapmux_lookup_path "$1"; then __lm_kind=external; fi
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

func cshLookup(name string, nativeOnly bool) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "set __lm_kind = missing\nset __lm_path = ''\nset __lm_name = %s\n", cshQuote(name))
	if !nativeOnly {
		builder.WriteString("set __lm_alias = \"`alias $__lm_name`\"\nif ( \"$__lm_alias\" != '' ) then\nset __lm_kind = alias\nelse\n")
	} else {
		builder.WriteString("unalias $__lm_name\n")
	}
	builder.WriteString("which $__lm_name >& /dev/null\nif ( $status == 0 ) then\nset __lm_path = \"`which $__lm_name`\"\nif ( -f \"$__lm_path\" && -x \"$__lm_path\" ) set __lm_kind = external\nendif\n")
	if !nativeOnly {
		builder.WriteString("endif\n")
	}
	return builder.String()
}

const nuLookupSetup = `
let __lm_version = (version | get version | split row '.')
let __lm_deferred = (($__lm_version.0 | into int) > 0 or ($__lm_version.1 | into int) >= 114)
def leapmux-lookup [name: string, allow_callable: bool] {
	which --all $name | where {|result|
		$result.type == 'external' or ($allow_callable and $result.type in ['alias', 'custom'])
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

func lookupSetup(shell string) string {
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
		return "unalias echo\nunalias which\nunalias alias\n"
	default:
		return posixLookupSetup(shell)
	}
}

// renderCommandProbe runs each lookup in the same profile and working directory.
// Metadata is line-framed separately from the profile's output.
func renderCommandProbe(shell, nonce string, names []string, nativeOnly bool) string {
	var script strings.Builder
	script.WriteString(lookupSetup(shell))
	if nativeOnly && terminal.IsPwsh(shell) {
		script.WriteString(pwshNativeLookupSetup())
	}
	script.WriteString(probeLine(shell, nonce+":start"))
	for index, name := range names {
		prefix := nonce + ":" + strconv.Itoa(index) + ":"
		script.WriteString(commandProbeResult(shell, name, prefix, nativeOnly))
	}
	script.WriteString(probeLine(shell, nonce+":complete"))
	return script.String()
}

func probeLine(shell, line string) string {
	quoted := shellquote.POSIX.Quote(line)
	switch {
	case terminal.IsPwsh(shell):
		return "[Console]::Out.WriteLine(''); [Console]::Out.WriteLine(" + shellquote.PowerShell.Quote(line) + ")\n"
	case shell == "nu":
		return "print ''; print " + nuQuote(line) + "\n"
	case shell == "xonsh":
		return "print(); print(" + pythonString(line) + ", flush=True)\n"
	case shell == "fish":
		return "builtin printf '\\n%s\\n' " + fishQuote(line) + "\n"
	case isCsh(shell):
		return "echo ''\necho " + cshQuote(line) + "\n"
	default:
		return posixBuiltin(shell, "printf") + " '\\n%s\\n' " + quoted + "\n"
	}
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
		return fmt.Sprintf("let __lm_result = (leapmux-lookup %s %s); if $__lm_result != null { print %s } else { print '%s' }\n",
			nuQuote(name), allow, value, absent)
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
		result := cshLookup(name, nativeOnly)
		if nativeOnly {
			return result + fmt.Sprintf("if ( \"$__lm_kind\" == external ) then\nif ( \"$__lm_path\" !~ /* ) set __lm_path = \"$cwd/$__lm_path\"\necho '%s:'\"$__lm_path\"\nelse\necho '%s'\nendif\n", present, absent)
		}
		return result + fmt.Sprintf("if ( \"$__lm_kind\" != missing ) then\necho '%s'\nelse\necho '%s'\nendif\n", present, absent)
	default:
		setup := "__leapmux_lookup " + quoted + " " + mode + "\n"
		if !nativeOnly {
			return setup + fmt.Sprintf("if [ \"$__lm_kind\" != missing ]; then %s '%%s\\n' '%s'; else %s '%%s\\n' '%s'; fi\n", posixBuiltin(shell, "printf"), present, posixBuiltin(shell, "printf"), absent)
		}
		normalize := `case "$__lm_path" in /*) ;; *) __lm_path="$(\command pwd -P)/$__lm_path";; esac` + "\n"
		if runtime.GOOS == "windows" {
			normalize = `case "$__lm_path" in /*) ;; *) __lm_path="$(\command pwd -P)/$__lm_path";; esac
if [ -x /usr/bin/cygpath ]; then __lm_path=$(/usr/bin/cygpath -w -- "$__lm_path") || exit 1; fi
`
		}
		return setup + "if [ \"$__lm_kind\" = external ]; then\n" + normalize +
			fmt.Sprintf("%s '%%s:%%s\\n' '%s' \"$__lm_path\"\nelse\n%s '%%s\\n' '%s'\nfi\n", posixBuiltin(shell, "printf"), present, posixBuiltin(shell, "printf"), absent)
	}
}
