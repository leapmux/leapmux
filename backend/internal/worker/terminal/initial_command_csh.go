package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (b *initialCommandBootstrap) cshStartup(env []string) ([]string, error) {
	home, err := startupHomeDirectory(env)
	if err != nil {
		return nil, fmt.Errorf("find the C shell home directory: %w", err)
	}
	for _, file := range []struct{ name, source string }{
		{".tcshrc", tcshStartup},
		{".cshrc", classicCshStartup},
		{".login", cshLoginStartup},
		{"handoff.csh", cshHandoff},
	} {
		source := strings.ReplaceAll(file.source, "LEAPMUX_SCHEDULED_ALIAS", "leapmux_initial_"+b.nonce)
		if err := os.WriteFile(filepath.Join(b.dir, file.name), []byte(source), 0o600); err != nil {
			return nil, fmt.Errorf("write the C shell startup file %q: %w", file.name, err)
		}
	}
	b.sourceCommand = func(command string) string {
		// BSD csh drops the last line of a sourced file without a newline.
		return cshCommandPrelude + command + "\n"
	}
	// The shell starts with a private home so it can find the bootstrap.
	// System profiles run before the bootstrap restores the real home.
	return append(env, "HOME="+b.dir, "LM_CSH_STARTUP="+b.dir, "LM_CSH_HOME="+home, "LM_CSH_HOME_ENV="+home), nil
}

// BSD csh keeps its shell home variable independent of setenv HOME.
const restoreCshHome = `if ( 1 ) set home = "$LM_CSH_HOME"
if ( $?LM_CSH_HOME_ENV ) then
    if ( 1 ) setenv HOME "$LM_CSH_HOME_ENV"
else
    if ( 1 ) unsetenv HOME
endif
`

const saveCshHome = `if ( 1 ) setenv LM_CSH_HOME "$home"
if ( $?HOME ) then
    if ( 1 ) setenv LM_CSH_HOME_ENV "$HOME"
else
    if ( 1 ) unsetenv LM_CSH_HOME_ENV
endif
if ( 1 ) set home = "$LM_CSH_STARTUP"
if ( 1 ) setenv HOME "$LM_CSH_STARTUP"
`

// After this file restores home, tcsh reads history, .login, and the saved
// directory stack from the original home. Its scheduled source runs before
// the first prompt, even when the login file defines its own precmd.
const tcshStartup = restoreCshHome + `if ( -f "$home/.tcshrc" && -r "$home/.tcshrc" ) then
    if ( 1 ) source "$home/.tcshrc"
else if ( -f "$home/.cshrc" && -r "$home/.cshrc" ) then
    if ( 1 ) source "$home/.cshrc"
endif
if ( 1 ) alias LEAPMUX_SCHEDULED_ALIAS 'if ( 1 ) source "$LM_CSH_STARTUP/handoff.csh"'
if ( 1 ) sched +0:00 LEAPMUX_SCHEDULED_ALIAS
`

// BSD csh has no scheduler. Keep the private home until its native history
// load and login file, and point its default history file at the original one.
const classicCshStartup = restoreCshHome + `if ( -f "$home/.cshrc" && -r "$home/.cshrc" ) then
    if ( 1 ) source "$home/.cshrc"
endif
if ( ! $?histfile && -f "$home/.history" ) /bin/ln -s "$home/.history" "$LM_CSH_STARTUP/.history"
` + saveCshHome

// A tcsh built with LOGINFIRST reads the private .login before its rc file.
// Restore the private home after that phase so tcsh can find the managed rc.
const cshLoginStartup = restoreCshHome + `if ( -f "$home/.login" && -r "$home/.login" ) then
    if ( 1 ) source "$home/.login"
endif
if ( $?tcsh ) then
` + saveCshHome + `else
    if ( 1 ) source "$LM_CSH_STARTUP/handoff.csh"
endif
`

// A builtin after a one-line conditional bypasses aliases from the profiles.
// The Worker publishes the full source file by rename after it records readiness.
// Native source semantics apply. An exit ends the sourced input, and an exec
// replaces the original shell process.
const cshHandoff = `if ( "$$" == "$LEAPMUX_INITIAL_COMMAND_PID" ) then
    if ( 1 ) unalias LEAPMUX_SCHEDULED_ALIAS
    if ( 1 ) echo "$$ $LEAPMUX_INITIAL_COMMAND_NONCE" >! "$LEAPMUX_INITIAL_COMMAND_READY"
    if ( $status == 0 ) then
        if ( 1 ) set lm_initial_polls = 0
        while ( ! -f "$LEAPMUX_INITIAL_COMMAND_FILE" && -f "$LEAPMUX_INITIAL_COMMAND_READY" && $lm_initial_polls < 1500 )
            /bin/sleep 0.02
            @ lm_initial_polls++
        end
        if ( 1 ) unset lm_initial_polls
        if ( -f "$LEAPMUX_INITIAL_COMMAND_FILE" ) source "$LEAPMUX_INITIAL_COMMAND_FILE"
    endif
endif
`

// The shell acknowledges after source opens the file. Its descriptor remains
// readable when the Worker removes the directory.
const cshCommandPrelude = `if ( 1 ) echo "$$ $LEAPMUX_INITIAL_COMMAND_NONCE" >! "$LEAPMUX_INITIAL_COMMAND_ACK"
if ( $status != 0 ) exit $status
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_FILE
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_ACK
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_READY
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_NONCE
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_PARENT
if ( 1 ) unsetenv LEAPMUX_INITIAL_COMMAND_PID
if ( 1 ) unsetenv LM_CSH_STARTUP
if ( 1 ) unsetenv LM_CSH_HOME
if ( 1 ) unsetenv LM_CSH_HOME_ENV
`
