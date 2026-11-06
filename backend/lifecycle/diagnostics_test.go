package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Attempt diagnostics obey the excerpt caps, survive a missing log,
// keep the previous attempt's log separate, and retire completed
// attempts down to the retention bound while the current one stays.
func TestStartupDiagnosticsAreAttemptSpecific(t *testing.T) {
	t.Run("an empty and a missing log read as empty excerpts", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.log"), nil, 0o600))
		got, err := ReadExcerpt(filepath.Join(dir, "empty.log"))
		require.NoError(t, err)
		assert.Empty(t, got)

		got, err = ReadExcerpt(filepath.Join(dir, "absent.log"))
		require.NoError(t, err, "a missing log must not fail the primary error")
		assert.Empty(t, got)
	})

	t.Run("a huge log keeps only its bounded tail", func(t *testing.T) {
		dir := t.TempDir()
		huge := filepath.Join(dir, "huge.log")
		var sb strings.Builder
		for i := range 10_000 {
			sb.WriteString("line-")
			sb.WriteString(strconv.Itoa(i))
			sb.WriteString("-with-padding-0123456789-")
			sb.WriteString(strings.Repeat("x", 20))
			sb.WriteString("\n")
		}
		require.NoError(t, os.WriteFile(huge, []byte(sb.String()), 0o600))

		got, err := ReadExcerpt(huge)
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(got), "\n")
		assert.LessOrEqual(t, len(lines), maxExcerptLines, "the excerpt holds at most 40 lines")
		assert.LessOrEqual(t, len(got), maxExcerptBytes+100, "the excerpt stays inside 32 KiB")
		assert.Contains(t, lines[len(lines)-1], "9999", "the tail keeps the LAST lines")
		assert.NotContains(t, lines[0], "line-0-", "the head is gone")
	})

	t.Run("each attempt's log is its own file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "logs-root")
		paths := pathsFor(mustCanonical(t, root))
		first, err := AttemptLog(paths, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		require.NoError(t, err)
		_, err = first.WriteString("first attempt failure\n")
		require.NoError(t, err)
		require.NoError(t, first.Close())

		second, err := AttemptLog(paths, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		require.NoError(t, err)
		_, err = second.WriteString("second attempt failure\n")
		require.NoError(t, err)
		require.NoError(t, second.Close())

		got, err := ReadExcerpt(paths.AttemptLogPath("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
		require.NoError(t, err)
		assert.Equal(t, "first attempt failure", got, "the previous attempt's log does not leak into the current one")
	})

	t.Run("rotation retires completed attempts and keeps the current one", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "rot-root")
		paths := pathsFor(mustCanonical(t, root))
		current := "cccccccccccccccccccccccccccccccc"
		require.NoError(t, os.MkdirAll(paths.Logs, 0o700))
		for i := range maxRetainedAttempts + 4 {
			id := strings.Repeat(string(rune('a'+i%26)), 32)
			// Distinct modification times, so the retirement order is
			// the completion order.
			path := paths.AttemptLogPath(id)
			require.NoError(t, os.WriteFile(path, []byte("done\n"), 0o600))
			past := time.Now().Add(-time.Duration(maxRetainedAttempts+10-i) * time.Minute)
			require.NoError(t, os.Chtimes(path, past, past))
		}
		currentLog, err := AttemptLog(paths, current)
		require.NoError(t, err)
		require.NoError(t, currentLog.Close())

		require.NoError(t, RotateAttemptLogs(paths, current))

		assert.FileExists(t, paths.AttemptLogPath(current), "the current attempt is never retired")
		entries, err := os.ReadDir(paths.Logs)
		require.NoError(t, err)
		assert.Equal(t, maxRetainedAttempts, len(entries),
			"the current attempt plus the retained completed attempts survive")
	})
}
