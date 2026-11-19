package launch

import "github.com/leapmux/leapmux/internal/worker/controlenv"

// The Worker puts the directory of its own `leapmux` first on the PATH of each
// process that it spawns (see controlenv.Apply). An agent starts inside the
// user's login shell, and the profile of that shell runs after the shell
// receives the environment. The profile can move the directory behind another
// `leapmux`, or drop it:
//
//   - macOS path_helper, which /etc/zprofile and /etc/profile run, moves every
//     system directory, such as /usr/local/bin, before it.
//   - Debian's /etc/profile assigns PATH in every bash and sh login shell.
//   - A profile that prepends a directory puts that directory first.
//
// The agent would then run a `leapmux` of another version, which cannot speak
// to the Worker. Each step below puts the directory of LEAPMUX_CONTROL_CLI
// first on PATH again, in one dialect. Each builder runs its step at the head
// of the launch environment, after the profile and before every statement
// that acts:
//
//   - Only setup runs before the step, such as the definitions of functions
//     and the statements that protect the builtins from the profile.
//   - It runs before StripEnvKeys and SetEnv, so a PATH that a provider pins
//     through SetEnv reaches the program unchanged.
//   - It runs before the candidate selection, so the lookup sees the
//     Worker's CLI first too, and before the program starts.
//
// Nushell has two builders, and both take the step from nuLaunchEnvironment.
// The csh command and the Git Bash command run from an invocation file, which
// holds the step.
//
// The step reads the variable when the shell runs, not when Wrap builds the
// command: a provider applies the control context to cmd.Env after Wrap
// returns (see providerkit.FinalizeAgentEnv). It applies the rules of
// controlenv.Apply for the directory of a fresh LEAPMUX_CONTROL_CLI:
//
//   - The variable must hold an absolute path. A relative directory would
//     resolve against the working directory of each command.
//   - A directory that holds the PATH separator stays off PATH, as
//     controlenv.PathEntryAllowed states.
//   - An unset PATH stays unset. A program receives PATH after the step only
//     when it would receive PATH without the step.
//   - The directory moves to the front. Every other entry, an empty one
//     included, keeps its place. csh and tcsh keep the other entries of the
//     directory too (see cshReprependControlCLI).
//
// TestWrapMovesTheControlCLIDirectoryAsControlenvDoes runs each installed
// shell over the same cases as controlenv.Apply and compares the results.
//
// Each step compares entries exactly, also on Windows, where controlenv
// ignores case. The entry that controlenv.Apply puts on PATH has the exact
// spelling of the directory, so the comparison finds it.

// controlCLIVariable is the variable that each step reads.
const controlCLIVariable = controlenv.CLIVariable

// posixReprependControlCLI returns the step for bash, zsh, sh, ash, dash and
// ksh. posixLaunchEnvironment runs it inside the block that protects the
// builtins from profile functions, and prefix is the prefix of that block
// (see posixBuiltinPrefix).
//
// It removes each entry of the directory from ":$PATH:" with the prefix and
// suffix removals of POSIX parameter expansion, which match the quoted
// directory literally, so a glob character in a path matches only itself. A
// result of ":" means that PATH held only the directory.
//
// It assigns PATH and never exports it. A shell that starts without PATH sets
// a default PATH of its own and does not export it, and a PATH from the
// environment stays exported when the step assigns it. A program therefore
// receives PATH only when it would receive PATH without the step.
//
// A profile can make PATH readonly, and an assignment to it then fails. dash,
// zsh and every shell that does not run interactively end the whole command,
// and an interactive bash or ksh reports the failure. So the step tries the
// assignment in a subshell first, and leaves a readonly PATH alone.
//
// Every temporary variable goes before the program starts, so a profile that
// runs `set -a` exports none of them.
//
// The step acts only on a path that starts with "/". On Windows, a POSIX shell
// such as Git Bash reads a Windows path, which it cannot put on its own PATH.
// The CLI still hands a `leapmux control` command to LEAPMUX_CONTROL_CLI there.
func posixReprependControlCLI(prefix string) string {
	return `case "${PATH+set}:${` + controlCLIVariable + `-}" in
set:/*)
	__lm_cli_dir="${` + controlCLIVariable + `%/*}"
	__lm_cli_dir="${__lm_cli_dir:-/}"
	case "$__lm_cli_dir" in
	*:*) ;;
	*)
		if (PATH="$PATH") 2>/dev/null; then
			__lm_cli_path=":$PATH:"
			while :; do
				case "$__lm_cli_path" in
				*:"$__lm_cli_dir":*) __lm_cli_path="${__lm_cli_path%%:"$__lm_cli_dir":*}:${__lm_cli_path#*:"$__lm_cli_dir":}";;
				*) break;;
				esac
			done
			case "$__lm_cli_path" in
			:) PATH="$__lm_cli_dir";;
			*) __lm_cli_path="${__lm_cli_path#:}"; PATH="$__lm_cli_dir:${__lm_cli_path%:}";;
			esac
		fi;;
	esac
	` + prefix + `unset __lm_cli_dir __lm_cli_path;;
esac
`
}

// cshReprependControlCLI is the step for tcsh and csh.
//
// It puts the directory first and leaves the entry that the profile moved, so
// PATH holds the directory twice. The first entry wins every lookup. Removing
// the other entry needs a split of PATH at each ":", and csh can make no exact
// split:
//
//   - The path array spells an empty entry of PATH as ".".
//   - No modifier cuts a string at a ":", and a word list splits each entry
//     that holds a space.
//
// So the step edits the PATH string with setenv, not the path array.
//
// It is several lines for the reason that cshEnvSeed states: csh substitutes
// each line's variables before it evaluates that line, so the guard on the
// first line must come before each read of the variable.
const cshReprependControlCLI = "if ( $?" + controlCLIVariable + " && $?PATH ) then\n" +
	"set __lm_cli_dir = \"\"\n" +
	"if ( \"$" + controlCLIVariable + "\" =~ /* ) set __lm_cli_dir = \"$" + controlCLIVariable + ":h\"\n" +
	"if ( \"$" + controlCLIVariable + "\" =~ /* && \"$__lm_cli_dir\" == \"\" ) set __lm_cli_dir = /\n" +
	"if ( \"$__lm_cli_dir\" =~ *:* ) set __lm_cli_dir = \"\"\n" +
	"if ( \"$__lm_cli_dir\" != \"\" ) setenv PATH \"${__lm_cli_dir}:${PATH}\"\n" +
	"endif\n"

// fishReprependControlCLI is the step for fish. fish keeps PATH as a list, so
// the step filters the list. Each variable is local to the block that sets it.
// `set --global` without --export or --unexport keeps the export state of
// PATH. fish itself spells an empty entry of PATH as ".". fish refuses a
// function named set, string or test, so the profile cannot replace them.
//
// The step ends with a status of 0. A successful `set` passes the status
// before it along, and each statement of fishClearEnv and fishSetEnv exits
// with the status of its `set`, so a status of 1 from the filter would end
// the launch. A profile can define a function named true, so the step calls
// the builtin.
const fishReprependControlCLI = `if set --query PATH; and string match --quiet -- '/*' "$` + controlCLIVariable + `"; ` +
	`set --local __lm_cli_dir (string replace --regex -- '/[^/]*$' '' "$` + controlCLIVariable + `" | string collect); ` +
	`test -n "$__lm_cli_dir"; or set __lm_cli_dir /; ` +
	`if not string match --quiet -- '*:*' "$__lm_cli_dir"; ` +
	`set --local __lm_cli_path; ` +
	`for __lm_cli_entry in $PATH; test "$__lm_cli_entry" = "$__lm_cli_dir"; or set --append __lm_cli_path "$__lm_cli_entry"; end; ` +
	`set --global PATH "$__lm_cli_dir" $__lm_cli_path; ` +
	`end; end; builtin true; `

// nuReprependControlCLI is the step for Nushell. `$env.NAME?` reads a variable
// that may be unset, and it ignores case, so it reads Path on Windows as well.
// `split row` accepts PATH as a list, which Nushell makes of it, and as a
// string. The pattern accepts a Unix path, a Windows path with a drive, and a
// UNC path. The `%` sigil calls each builtin even when the profile defines a
// command of the same name. `where` is a keyword, which no profile can define,
// and which takes no sigil.
const nuReprependControlCLI = `let __lm_cli = ($env.` + controlCLIVariable + `? | %default ''); ` +
	`let __lm_cli_dir = ($__lm_cli | %path dirname); ` +
	`if ($env.PATH? != null) and ($__lm_cli =~ '^(/|[A-Za-z]:[\\/]|[\\/][\\/])') and not ($__lm_cli_dir | %str contains (%char esep)) { ` +
	`$env.PATH = ($env.PATH | %split row (%char esep) | where {|entry| $entry != $__lm_cli_dir } | %prepend $__lm_cli_dir) }; `

// pwshReprependControlCLI is the step for PowerShell 7 and Windows PowerShell
// 5.1, which reads $env:PATH regardless of case on Windows. It uses only .NET
// members that both versions have. buildPwshCommand runs it before it sets
// $ErrorActionPreference, so a failure in it reports an error and the launch
// goes on.
const pwshReprependControlCLI = `$__lm_cli = [string]$env:` + controlCLIVariable + `; ` +
	`if ($null -ne $env:PATH -and $__lm_cli -match '^(/|[A-Za-z]:[\\/]|[\\/][\\/])') { ` +
	`$__lm_cli_dir = [System.IO.Path]::GetDirectoryName($__lm_cli); ` +
	`$__lm_separator = [string][System.IO.Path]::PathSeparator; ` +
	`if ($__lm_cli_dir -and -not $__lm_cli_dir.Contains($__lm_separator)) { ` +
	`$env:PATH = (@($__lm_cli_dir) + @($env:PATH.Split([char]$__lm_separator) | Where-Object { $_ -cne $__lm_cli_dir })) -join $__lm_separator } }; `

// xonshReprependControlCLI is the step for xonsh. It is Python, and it follows
// xonshLookupSetup, which imports os as __lm_os.
//
// It reads the environment that a program would receive, which detype gives:
//
//   - xonsh gives itself a default PATH when it starts without one, and it
//     passes that PATH on, so the step acts on it as on any other PATH.
//   - A profile that runs `del $PATH` leaves no PATH to pass on, although
//     `"PATH" in __xonsh__.env` stays true, because PATH has a default. The
//     step then leaves PATH unset.
//
// The string of the detyped PATH holds each entry, an empty one included.
// The EnvPath that __xonsh__.env holds drops the one empty entry of an empty
// PATH, so the step splits the string. It assigns a list, which xonsh keeps as
// an EnvPath with the same entries.
//
// xonsh expands `$NAME` and a leading `~` in each entry of PATH when it passes
// PATH on, with the step or without it. An absolute directory has no leading
// `~`, and a directory that holds `$` and the name of a set variable reaches
// the program expanded, as every entry of the profile does.
//
// `xonsh -c` prints the value of an expression statement, so the step holds
// only assignments and if statements.
const xonshReprependControlCLI = "__lm_cli_environ = __xonsh__.env.detype()\n" +
	"__lm_cli = __lm_cli_environ.get(\"" + controlCLIVariable + "\", \"\")\n" +
	"if \"PATH\" in __lm_cli_environ and __lm_os.path.isabs(__lm_cli):\n" +
	"    __lm_cli_dir = __lm_os.path.dirname(__lm_cli)\n" +
	"    if __lm_os.pathsep not in __lm_cli_dir:\n" +
	"        __xonsh__.env[\"PATH\"] = [__lm_cli_dir] + [__lm_entry for __lm_entry in __lm_cli_environ[\"PATH\"].split(__lm_os.pathsep) if __lm_entry != __lm_cli_dir]\n"
