package pathutil

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// One table for the rule the backend used to state four times, in four places
// that disagreed. The cases are the union of the three it replaces:
// config_test.go's TestExpandHome, service_test.go's expandTilde table, and
// sessionstore_test.go's TestExpandHome.
func TestExpandHome(t *testing.T) {
	t.Parallel()

	const home = "/home/u"
	tests := []struct {
		name string
		path string
		home string
		want string
	}{
		{"tilde alone", "~", home, home},
		{"tilde with a path", "~/some/path", home, filepath.Join(home, "some/path")},
		{"a store's config value", "~/.codex", home, filepath.Join(home, ".codex")},
		{"absolute path unchanged", "/absolute/path", home, "/absolute/path"},
		{"relative path unchanged", "relative/path", home, "relative/path"},
		{"empty path", "", home, ""},
		// Shell conventions no store or configuration file here writes.
		// Guessing at them would resolve to a home that is not the one named.
		{"double tilde unchanged", "~~", home, "~~"},
		{"tilde in the middle unchanged", "/foo/~/bar", home, "/foo/~/bar"},
		{"another user's home unchanged", "~user/foo", home, "~user/foo"},
		{"another user's home, no slash", "~other/x", home, "~other/x"},
		// A caller whose own home lookup failed degrades to the literal value
		// rather than to the filesystem root.
		{"no home leaves the path alone", "~/x", "", "~/x"},
		{"no home leaves a bare tilde alone", "~", "", "~"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ExpandHome(tt.path, tt.home),
				"ExpandHome(%q, %q)", tt.path, tt.home)
		})
	}
}

// `~\` is a home reference on Windows and an ordinary filename on POSIX, where
// a backslash is a legal character in a component. Expanding it there would
// invent a directory the user never wrote. validate.SanitizePath draws the same
// line, and this is the assertion that keeps the two together.
func TestExpandHome_BackslashIsWindowsOnly(t *testing.T) {
	t.Parallel()

	const home = `/home/u`
	got := ExpandHome(`~\sub`, home)
	if runtime.GOOS == "windows" {
		assert.Equal(t, filepath.Join(home, "sub"), got)
		return
	}
	assert.Equal(t, `~\sub`, got, "a backslash is an ordinary character in a POSIX filename")
}
