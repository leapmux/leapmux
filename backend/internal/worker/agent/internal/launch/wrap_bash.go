package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/shellquote"
)

// Use native special-builtin precedence for sourcing even if a profile
// defines its own dot command. Restore the original Bash mode in the file.
const bashInvocationPrefix = `case ":$SHELLOPTS:" in *:posix:*) __lm_restore_source_mode=0;; *) POSIXLY_CORRECT=y; __lm_restore_source_mode=1;; esac; '.' `

const bashInvocationSetup = `case "$__lm_restore_source_mode" in
1) 'unset' __lm_restore_source_mode; 'set' +o posix;;
*) 'unset' __lm_restore_source_mode;;
esac
`

// Git Bash's MSYS startup parser collapses some backslashes in Win32
// command-line arguments. Read the generated script from a file after the
// login profile runs so the script and the provider's stdin stay intact.
func (p *PreparedLaunch) writeBashInvocation(script string) (string, error) {
	dir, err := p.makeInvocationDirectory("leapmux-bash-launch-")
	if err != nil {
		return "", fmt.Errorf("create the Git Bash invocation directory: %w", err)
	}
	path := filepath.Join(dir, "launch.sh")
	if err := os.WriteFile(path, []byte(bashInvocationSetup+script), 0o600); err != nil {
		return "", errors.Join(fmt.Errorf("write the Git Bash invocation: %w", err), p.Close())
	}
	return bashInvocationPrefix + shellquote.POSIX.Quote(filepath.ToSlash(path)), nil
}
