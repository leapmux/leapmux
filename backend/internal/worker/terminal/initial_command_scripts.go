package terminal

// The original shell requests a release and waits while the Worker registers
// the PTY and authorizes this launch. It loads the full command before the
// final acknowledgement, after which the Worker can remove the launch files.
const posixInitialCommand = `
if (set -C; printf '%s %s\n' "$LEAPMUX_INITIAL_COMMAND_PID" "$LEAPMUX_INITIAL_COMMAND_NONCE" > "$LEAPMUX_INITIAL_COMMAND_READY"); then
  __leapmux_waits=0
  while [ ! -f "$LEAPMUX_INITIAL_COMMAND_FILE" ] && [ -f "$LEAPMUX_INITIAL_COMMAND_READY" ] &&
        [ "$__leapmux_waits" -lt 1500 ]; do
    command sleep 0.02 || break
    __leapmux_waits=$((__leapmux_waits + 1))
  done
  unset __leapmux_waits
  if [ -f "$LEAPMUX_INITIAL_COMMAND_FILE" ] &&
     __leapmux_command="$(command cat -- "$LEAPMUX_INITIAL_COMMAND_FILE" && printf .)"; then
    __leapmux_command=${__leapmux_command%.}
    if (set -C; printf '%s %s\n' "$LEAPMUX_INITIAL_COMMAND_PID" "$LEAPMUX_INITIAL_COMMAND_NONCE" > "$LEAPMUX_INITIAL_COMMAND_ACK"); then
      unset LEAPMUX_INITIAL_COMMAND_FILE LEAPMUX_INITIAL_COMMAND_READY LEAPMUX_INITIAL_COMMAND_ACK LEAPMUX_INITIAL_COMMAND_NONCE LEAPMUX_INITIAL_COMMAND_PID LEAPMUX_INITIAL_COMMAND_PARENT
      eval "$__leapmux_command"
    fi
    unset __leapmux_command
  fi
fi
`

const restoreStartupEnvironment = `
if [ "${LEAPMUX_INITIAL_COMMAND_HAS_ENV:-}" = 1 ]; then
  ENV=$LEAPMUX_INITIAL_COMMAND_ORIGINAL_ENV
  export ENV
else
  unset ENV
fi
unset LEAPMUX_INITIAL_COMMAND_ORIGINAL_ENV LEAPMUX_INITIAL_COMMAND_HAS_ENV
`

const bashInitialStartup = restoreStartupEnvironment + `
set +o posix
if [ -r /etc/profile ]; then
  . /etc/profile
fi
for __leapmux_profile in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
  if [ -r "$__leapmux_profile" ]; then
    . "$__leapmux_profile"
    break
  fi
done
unset __leapmux_profile
if [[ ${BASH_SUBSHELL:-1} = 0 && $$ = "$LEAPMUX_INITIAL_COMMAND_PID" ]]; then
` + posixInitialCommand + `
fi
`

const restoreZshProfileDirectory = `
if [[ ${LEAPMUX_INITIAL_COMMAND_HAS_ZDOTDIR:-} = 1 ]]; then
  export ZDOTDIR=$LEAPMUX_INITIAL_COMMAND_ORIGINAL_ZDOTDIR
else
  unset ZDOTDIR
fi
`

const saveZshProfileDirectory = `
if [[ -n ${ZDOTDIR+x} ]]; then
  LEAPMUX_INITIAL_COMMAND_HAS_ZDOTDIR=1
  LEAPMUX_INITIAL_COMMAND_ORIGINAL_ZDOTDIR=$ZDOTDIR
else
  unset LEAPMUX_INITIAL_COMMAND_HAS_ZDOTDIR LEAPMUX_INITIAL_COMMAND_ORIGINAL_ZDOTDIR
fi
export ZDOTDIR=$LEAPMUX_INITIAL_COMMAND_MANAGED_ZDOTDIR
`

// Run in file scope. Zsh would make typeset declarations local inside a
// precmd function, even when the command runs in the same process.
func zshInitialStartup(profile string, final bool) string {
	originalFile := `"${ZDOTDIR-$HOME}/` + profile + `"`
	sourceProfile := "if [[ -r " + originalFile + " ]]; then\n  source " + originalFile + "\nfi\n"
	handoff := `
unset LEAPMUX_INITIAL_COMMAND_ORIGINAL_ZDOTDIR LEAPMUX_INITIAL_COMMAND_HAS_ZDOTDIR LEAPMUX_INITIAL_COMMAND_MANAGED_ZDOTDIR
if [[ $ZSH_SUBSHELL = 0 && $$ = "$LEAPMUX_INITIAL_COMMAND_PID" ]]; then
` + posixInitialCommand + "\nfi\n"
	if final {
		return restoreZshProfileDirectory + sourceProfile + handoff
	}
	return restoreZshProfileDirectory + sourceProfile + "\nif [[ -o RCS ]]; then\n" + saveZshProfileDirectory + "\nelse\n" + handoff + "\nfi\n"
}

const posixInitialStartup = restoreStartupEnvironment + `
if [ -n "${ENV:-}" ]; then
  # An unquoted here-document expands ENV as data. Quotes in a literal
  # filename cannot finish the shell statement that expands it.
  __leapmux_delimiter=LEAPMUX_$LEAPMUX_INITIAL_COMMAND_NONCE
  case "$ENV" in
    *"$__leapmux_delimiter"*) return ;;
  esac
  __leapmux_profile="$(eval "command cat <<$__leapmux_delimiter
$ENV.
$__leapmux_delimiter
")"
  __leapmux_profile=${__leapmux_profile%.}
  if [ -r "$__leapmux_profile" ]; then
    . "$__leapmux_profile"
  fi
  unset __leapmux_profile __leapmux_delimiter
fi
if [ "$$" = "$LEAPMUX_INITIAL_COMMAND_PID" ] &&
   /bin/sh -c 'printf "%s\n" "$PPID"' > "$LEAPMUX_INITIAL_COMMAND_PARENT" &&
   IFS= read -r __leapmux_parent < "$LEAPMUX_INITIAL_COMMAND_PARENT" &&
   [ "$__leapmux_parent" = "$LEAPMUX_INITIAL_COMMAND_PID" ]; then
` + posixInitialCommand + `
fi
unset __leapmux_parent
`

const fishInitialCommand = `
if test "$fish_pid" = "$LEAPMUX_INITIAL_COMMAND_PID"; and not test -e "$LEAPMUX_INITIAL_COMMAND_READY"
  printf '%s %s\n' "$fish_pid" "$LEAPMUX_INITIAL_COMMAND_NONCE" > "$LEAPMUX_INITIAL_COMMAND_READY"
  set -l __leapmux_waits 0
  while not test -f "$LEAPMUX_INITIAL_COMMAND_FILE"; and test -f "$LEAPMUX_INITIAL_COMMAND_READY"; and test $__leapmux_waits -lt 1500
    command sleep 0.02; or break
    set __leapmux_waits (math $__leapmux_waits + 1)
  end
  if test -f "$LEAPMUX_INITIAL_COMMAND_FILE"
    set -g __leapmux_command (string collect --allow-empty --no-trim-newlines < "$LEAPMUX_INITIAL_COMMAND_FILE")
    and printf '%s %s\n' "$fish_pid" "$LEAPMUX_INITIAL_COMMAND_NONCE" > "$LEAPMUX_INITIAL_COMMAND_ACK"
    or set -e __leapmux_command
  end
end
set -e LEAPMUX_INITIAL_COMMAND_FILE LEAPMUX_INITIAL_COMMAND_READY LEAPMUX_INITIAL_COMMAND_ACK LEAPMUX_INITIAL_COMMAND_NONCE LEAPMUX_INITIAL_COMMAND_PID LEAPMUX_INITIAL_COMMAND_PARENT
eval "$__leapmux_command"
set -e __leapmux_command
`

const powerShellInitialCommand = `
if (-not [IO.File]::Exists($env:LEAPMUX_INITIAL_COMMAND_READY)) {
  [IO.File]::WriteAllText($env:LEAPMUX_INITIAL_COMMAND_READY, "$PID $env:LEAPMUX_INITIAL_COMMAND_NONCE")
  $__leapmux_wait = [Diagnostics.Stopwatch]::StartNew()
  while (-not [IO.File]::Exists($env:LEAPMUX_INITIAL_COMMAND_FILE) -and
         [IO.File]::Exists($env:LEAPMUX_INITIAL_COMMAND_READY) -and $__leapmux_wait.ElapsedMilliseconds -lt 30000) {
    Start-Sleep -Milliseconds 20
  }
  if ([IO.File]::Exists($env:LEAPMUX_INITIAL_COMMAND_FILE)) {
    $__leapmux_command = [IO.File]::ReadAllText($env:LEAPMUX_INITIAL_COMMAND_FILE)
    [IO.File]::WriteAllText($env:LEAPMUX_INITIAL_COMMAND_ACK, "$PID $env:LEAPMUX_INITIAL_COMMAND_NONCE")
    Remove-Item Env:LEAPMUX_INITIAL_COMMAND_FILE,Env:LEAPMUX_INITIAL_COMMAND_READY,Env:LEAPMUX_INITIAL_COMMAND_ACK,Env:LEAPMUX_INITIAL_COMMAND_NONCE,Env:LEAPMUX_INITIAL_COMMAND_PARENT
    Invoke-Expression $__leapmux_command
    Remove-Variable __leapmux_command
  }
  Remove-Variable __leapmux_wait
}
`
