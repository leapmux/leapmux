package pathutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ExpandHome resolves a leading `~` in a path against `home`.
//
// `~` alone becomes `home`, and `~/rest` becomes `home/rest`. On Windows `~\`
// is accepted as well, matching validate.SanitizePath. It is NOT accepted on
// POSIX, where a backslash is an ordinary character in a filename, so `~\x` is
// one legitimate component and rewriting it would invent a directory the user
// never wrote.
//
// Every other form is returned unchanged, `~user/` and `~~` among them. Those
// are shell conventions that no store or configuration file here writes, and
// guessing at them would resolve a path to a home that is not the one named.
//
// `home` is a PARAMETER rather than an `os.UserHomeDir()` call inside, because
// the callers disagree about where the home comes from: an agent session reader
// takes it from the query that a test injects, and the configuration loader
// takes it from the process. A helper that resolved it itself could serve only
// the second, which is how this repo came to hold four copies of the rule. An
// empty `home` returns the path unchanged, so a caller whose lookup failed
// degrades to the literal value rather than to the filesystem root.
func ExpandHome(path, home string) string {
	if path == "" || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
		return filepath.Join(home, path[2:])
	}
	return path
}
