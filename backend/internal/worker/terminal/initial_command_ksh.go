package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (b *initialCommandBootstrap) kornShellStartup(ctx context.Context, shell string, env []string) ([]string, error) {
	// A ksh executable can select different KornShell variants. Ask the shell
	// without passing it startup profiles or credentials.
	probe := exec.CommandContext(ctx, shell, "-c", `command printf '%s' "${KSH_VERSION-}"`)
	probe.Env = []string{"ENV=", "BASH_ENV="}
	version, err := probe.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("identify the KornShell executable %q: %w", ShellBaseName(shell), err)
	}
	switch engine := string(version); {
	case strings.Contains(engine, "MIRBSD KSH "):
		return b.mkshStartup(env)
	case strings.HasPrefix(engine, "Version ") && strings.Contains(engine, " 93"):
		return b.ksh93Startup(env)
	default:
		return nil, fmt.Errorf("shell %q must run KornShell 93 or mksh for an initial command", ShellBaseName(shell))
	}
}

func (b *initialCommandBootstrap) ksh93Startup(env []string) ([]string, error) {
	login := filepath.Join(b.dir, "ksh-login")
	interactive := filepath.Join(b.dir, "ksh-interactive")
	if err := os.WriteFile(login, []byte(kshLoginStartup), 0o600); err != nil {
		return nil, fmt.Errorf("write the KornShell login hook: %w", err)
	}
	if err := os.WriteFile(interactive, []byte(kshInteractiveStartup), 0o600); err != nil {
		return nil, fmt.Errorf("write the KornShell interactive hook: %w", err)
	}
	return append(saveOriginalStartupEnvironment(env),
		"LEAPMUX_KSH_LOGIN="+login, "LEAPMUX_KSH_INTERACTIVE="+interactive, "ENV="+kshEnvironmentSelector,
	), nil
}

// KornShell expands ENV before it loads the system interactive profile.
// Its same-process substitution keeps login state in the original shell.
// Send profile output to the PTY's stderr so stdout holds only the hook path.
const kshEnvironmentSelector = `${ . "$LEAPMUX_KSH_LOGIN" >&2; command printf '%s' "${__leapmux_ksh_hook:-}"; }`

const kshLoginStartup = restoreStartupEnvironment + `
if [ -z "${ENV+x}" ] && [ -n "${HOME:-}" ]; then
  ENV=$HOME/.kshrc
fi
` + posixLoginProfiles + `
__leapmux_ksh_profile=
if [ -n "${ENV:-}" ]; then
  __leapmux_template=$ENV
  case "$__leapmux_template" in
    \~*)
      __leapmux_tilde=${__leapmux_template%%/*}
      case "$__leapmux_tilde" in
        *[!a-zA-Z0-9_~.+-]*) ;;
        *)
          eval "__leapmux_tilde_path=$__leapmux_tilde"
          __leapmux_template='${__leapmux_tilde_path}'${__leapmux_template#"$__leapmux_tilde"}
          ;;
      esac
      ;;
  esac
  __leapmux_delimiter=LEAPMUX_$LEAPMUX_INITIAL_COMMAND_NONCE
  case "$__leapmux_template" in
    *"$__leapmux_delimiter"*) return ;;
  esac
  # The here-document expands ENV without interpreting quotes as syntax.
  # KornShell's read keeps the full path, including spaces and newlines.
  eval "IFS= read -r -d '' __leapmux_ksh_profile <<$__leapmux_delimiter
$__leapmux_template.
$__leapmux_delimiter
" || :
  __leapmux_ksh_profile=${__leapmux_ksh_profile%.*}
  unset __leapmux_delimiter __leapmux_template __leapmux_tilde __leapmux_tilde_path
fi
case "$__leapmux_ksh_profile" in
  # KornShell's /./ and ././ prefixes suppress the system profile.
  /./*|././*) __leapmux_ksh_hook=/.$LEAPMUX_KSH_INTERACTIVE ;;
  *) __leapmux_ksh_hook=$LEAPMUX_KSH_INTERACTIVE ;;
esac
`

const kshInteractiveStartup = `
unset LEAPMUX_KSH_INTERACTIVE LEAPMUX_KSH_LOGIN __leapmux_ksh_hook
if [ -n "${__leapmux_ksh_profile:-}" ] && [ -r "$__leapmux_ksh_profile" ]; then
  . "$__leapmux_ksh_profile"
fi
unset __leapmux_ksh_profile
` + posixInitialHandoff
