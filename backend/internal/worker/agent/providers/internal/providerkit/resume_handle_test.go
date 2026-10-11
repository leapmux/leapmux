package providerkit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionFileHandleIsPath pins the shape test that picks the rule. It must
// answer the way the CLIs' own resolvers do: a separator anywhere, or the
// `.jsonl` suffix.
func TestSessionFileHandleIsPath(t *testing.T) {
	t.Parallel()

	assert.True(t, SessionFileHandleIsPath("/tmp/s.jsonl"))
	assert.True(t, SessionFileHandleIsPath(`C:\pi\s.jsonl`))
	assert.True(t, SessionFileHandleIsPath("s.jsonl"), "the suffix alone makes it a path")
	assert.True(t, SessionFileHandleIsPath("~/s"), "the separator alone makes it a path")
	assert.False(t, SessionFileHandleIsPath("018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"))
	assert.False(t, SessionFileHandleIsPath("~"), "a bare tilde holds no separator")
	assert.False(t, SessionFileHandleIsPath(""))
}

func TestResolveSessionFileOrIDHandle(t *testing.T) {
	t.Parallel()

	t.Run("accepts the empty handle as no resume", func(t *testing.T) {
		resolved, err := ResolveSessionFileOrIDHandle("", "")
		require.NoError(t, err)
		assert.Empty(t, resolved)
	})

	t.Run("an id takes the token rule and comes back unchanged", func(t *testing.T) {
		resolved, err := ResolveSessionFileOrIDHandle("01a0cf7f-6b10-77e8-9512-608ced3682b2", "")
		require.NoError(t, err)
		assert.Equal(t, "01a0cf7f-6b10-77e8-9512-608ced3682b2", resolved)

		_, err = ResolveSessionFileOrIDHandle("--dangerously-skip-permissions", "")
		assert.Error(t, err, "the token rule refuses a leading hyphen")
		_, err = ResolveSessionFileOrIDHandle(strings.Repeat("a", 129), "")
		assert.Error(t, err, "the token rule caps the length")
	})

	t.Run("a path past the token cap is accepted and cleaned", func(t *testing.T) {
		root := testutil.NativeAbsPath("/tmp/omp/sessions")
		typed := filepath.Join(root, strings.Repeat("a", 140)+".jsonl") + "  "
		resolved, err := ResolveSessionFileOrIDHandle(typed, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(root, strings.Repeat("a", 140)+".jsonl"), resolved,
			"the value that reaches argv is the value the rule approved, trimmed")
	})

	t.Run("a relative path is refused", func(t *testing.T) {
		_, err := ResolveSessionFileOrIDHandle("relative/s.jsonl", "")
		assert.Error(t, err)
	})

	t.Run("a path over the byte cap is refused", func(t *testing.T) {
		root := testutil.NativeAbsPath("/tmp")
		_, err := ResolveSessionFileOrIDHandle(filepath.Join(root, strings.Repeat("a", contracts.SessionFilePathByteLimit)+".jsonl"), "")
		assert.ErrorContains(t, err, "at most")
	})

	t.Run("an invisible character in a path is refused", func(t *testing.T) {
		root := testutil.NativeAbsPath("/tmp")
		_, err := ResolveSessionFileOrIDHandle(root+string(filepath.Separator)+"a\u200Bb.jsonl", "")
		assert.Error(t, err)
	})
}

// The byte cap applies to the handle as the user typed it: a path that takes
// the whole cap is accepted, and one byte more is refused.
func TestResolveSessionFileOrIDHandleCapsThePathAtItsLimit(t *testing.T) {
	t.Parallel()
	root := testutil.NativeAbsPath("/tmp")
	stem := root + string(filepath.Separator)
	fill := contracts.SessionFilePathByteLimit - len(stem) - len(".jsonl")
	require.Positive(t, fill)
	exact := stem + strings.Repeat("a", fill) + ".jsonl"
	require.Len(t, exact, contracts.SessionFilePathByteLimit)

	resolved, err := ResolveSessionFileOrIDHandle(exact, "")
	require.NoError(t, err, "a path that takes the whole cap is accepted")
	assert.Equal(t, exact, resolved)

	_, err = ResolveSessionFileOrIDHandle(stem+strings.Repeat("a", fill+1)+".jsonl", "")
	assert.ErrorContains(t, err, "at most")
}

// The value that reaches argv is the one that SanitizePath approved: `~`
// expands against the home directory, and a control character is dropped. A
// CLI opens a session file that does not exist as a new session, so the typed
// string would start an empty session.
func TestResolveSessionFileOrIDHandleReturnsTheSanitizedPath(t *testing.T) {
	t.Parallel()
	home := testutil.NativeAbsPath("/home/u")

	resolved, err := ResolveSessionFileOrIDHandle("~/sessions/s.jsonl", home)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "sessions", "s.jsonl"), resolved)

	typed := filepath.Join(home, "sessions", "a\x01b.jsonl")
	resolved, err = ResolveSessionFileOrIDHandle(typed, home)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "sessions", "ab.jsonl"), resolved)

	_, err = ResolveSessionFileOrIDHandle("s.jsonl", home)
	assert.Error(t, err, "the suffix makes a path, and a relative path is refused")
}
