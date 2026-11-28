package terminal

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type kornShellVariant uint8

const (
	kornShellUnknown kornShellVariant = iota
	kornShellMksh
	kornShellKsh93
)

type shellBinaryKey struct {
	absPath      string
	resolvedPath string
	size         int64
	modTimeNano  int64
	mode         fs.FileMode
}

const kornShellCacheLimit = 64

var (
	kornShellCacheMu sync.RWMutex
	kornShellCache   = make(map[shellBinaryKey]kornShellVariant)
)

func shellBinaryIdentity(shell string) (shellBinaryKey, bool) {
	absPath, err := filepath.Abs(shell)
	if err != nil {
		return shellBinaryKey{}, false
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return shellBinaryKey{}, false
	}
	info, err := os.Stat(resolvedPath)
	if err != nil || !info.Mode().IsRegular() {
		return shellBinaryKey{}, false
	}
	return shellBinaryKey{
		absPath:      absPath,
		resolvedPath: resolvedPath,
		size:         info.Size(),
		modTimeNano:  info.ModTime().UnixNano(),
		mode:         info.Mode(),
	}, true
}

func identifyKornShell(ctx context.Context, shell string) (kornShellVariant, error) {
	key, cacheable := shellBinaryIdentity(shell)
	if cacheable {
		kornShellCacheMu.RLock()
		cached, ok := kornShellCache[key]
		kornShellCacheMu.RUnlock()
		if ok {
			return cached, nil
		}
	}

	// A ksh executable can select different KornShell variants. Ask the shell
	// without passing it startup profiles or credentials.
	probe := exec.CommandContext(ctx, shell, "-c", `command printf '%s' "${KSH_VERSION-}"`)
	probe.Env = []string{"ENV=", "BASH_ENV="}
	version, err := probe.Output()
	if err != nil {
		if ctx.Err() != nil {
			return kornShellUnknown, ctx.Err()
		}
		return kornShellUnknown, fmt.Errorf("identify the KornShell executable %q: %w", ShellBaseName(shell), err)
	}
	var variant kornShellVariant
	switch engine := string(version); {
	case strings.Contains(engine, "MIRBSD KSH "):
		variant = kornShellMksh
	case strings.HasPrefix(engine, "Version ") && strings.Contains(engine, " 93"):
		variant = kornShellKsh93
	default:
		return kornShellUnknown, fmt.Errorf("shell %q must run KornShell 93 or mksh for an initial command", ShellBaseName(shell))
	}
	if cacheable {
		kornShellCacheMu.Lock()
		if len(kornShellCache) >= kornShellCacheLimit {
			clear(kornShellCache)
		}
		kornShellCache[key] = variant
		kornShellCacheMu.Unlock()
	}
	return variant, nil
}

func (b *initialCommandBootstrap) kornShellStartup(ctx context.Context, shell string, env []string) ([]string, error) {
	variant, err := identifyKornShell(ctx, shell)
	if err != nil {
		return nil, err
	}
	switch variant {
	case kornShellMksh:
		return b.mkshStartup(env)
	case kornShellKsh93:
		return b.ksh93Startup(env)
	default:
		return nil, fmt.Errorf("shell %q must run KornShell 93 or mksh for an initial command", ShellBaseName(shell))
	}
}

func (b *initialCommandBootstrap) ksh93Startup(env []string) ([]string, error) {
	login, err := b.writeFile("ksh-login", kshLoginStartup, "KornShell login hook")
	if err != nil {
		return nil, err
	}
	interactive, err := b.writeFile("ksh-interactive", kshInteractiveStartup, "KornShell interactive hook")
	if err != nil {
		return nil, err
	}
	return append(saveOriginalStartupEnvironment(env),
		initialCommandEnvKshLogin+"="+login,
		initialCommandEnvKshInteractive+"="+interactive,
		"ENV="+kshEnvironmentSelector,
	), nil
}

// KornShell expands ENV before it loads the system interactive profile.
// Its same-process substitution keeps login state in the original shell.
// Send profile output to the PTY's stderr so stdout holds only the hook path.
const kshEnvironmentSelector = `${ . "$LEAPMUX_INITIAL_COMMAND_KSH_LOGIN" >&2; command printf '%s' "${__leapmux_ksh_hook:-}"; }`

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
    *"$__leapmux_delimiter"*) ;;
    *)
      # The here-document expands ENV without interpreting quotes as syntax.
      # The trailing dot preserves embedded newlines across command substitution.
      __leapmux_ksh_profile="$(eval "command cat <<$__leapmux_delimiter
$__leapmux_template.
$__leapmux_delimiter
")"
      __leapmux_ksh_profile=${__leapmux_ksh_profile%.}
      ;;
  esac
  unset __leapmux_delimiter __leapmux_template __leapmux_tilde __leapmux_tilde_path
fi
case "$__leapmux_ksh_profile" in
  # KornShell's /./ and ././ prefixes suppress the system profile.
  /./*|././*) __leapmux_ksh_hook=/.$LEAPMUX_INITIAL_COMMAND_KSH_INTERACTIVE ;;
  *) __leapmux_ksh_hook=$LEAPMUX_INITIAL_COMMAND_KSH_INTERACTIVE ;;
esac
`

const kshInteractiveStartup = `
unset LEAPMUX_INITIAL_COMMAND_KSH_INTERACTIVE LEAPMUX_INITIAL_COMMAND_KSH_LOGIN __leapmux_ksh_hook
if [ -n "${__leapmux_ksh_profile:-}" ]; then
  case "$__leapmux_ksh_profile" in
    /*) ;;
    *) __leapmux_ksh_profile=./$__leapmux_ksh_profile ;;
  esac
  if [ -r "$__leapmux_ksh_profile" ]; then
    . "$__leapmux_ksh_profile"
  fi
fi
unset __leapmux_ksh_profile
` + posixInitialHandoff
