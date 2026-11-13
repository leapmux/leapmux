package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

func TestAttemptLogRotatesBySize(t *testing.T) {
	paths := pathsFor(mustCanonical(t, t.TempDir()))
	const attempt = "a9a0425886d148f68a8f94716ed47a78"
	writer, err := AttemptLog(paths, attempt)
	require.NoError(t, err)
	data := strings.Repeat("x", 9<<20) + "\nlatest failure\n"
	n, err := writer.WriteString(data)
	require.NoError(t, err)
	assert.Equal(t, len(data), n)
	require.NoError(t, writer.Close())
	path := paths.AttemptLogPath(attempt)
	for _, segment := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(segment)
		require.NoError(t, err)
		assert.LessOrEqual(t, info.Size(), int64(4<<20))
	}
	assert.NoFileExists(t, path+".3")
	excerpt, err := ReadExcerpt(path)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(excerpt, "latest failure"))
	assert.LessOrEqual(t, len(excerpt), 32<<10)
}

func TestReadExcerptAllocatesOnlyForTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	require.NoError(t, atomicfile.WriteFileDurable(path, nil, 0o600))
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(32<<20))
	_, err = file.WriteAt([]byte("\nlast startup failure\n"), (32<<20)-22)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	excerpt, err := ReadExcerpt(path)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	assert.Contains(t, excerpt, "last startup failure")
	assert.LessOrEqual(t, len(excerpt), 32<<10)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(512<<10), "read only the tail of a large log")
}

func TestAttemptLogConcurrentWritesAndFailures(t *testing.T) {
	paths := pathsFor(mustCanonical(t, t.TempDir()))
	const attempt = "a9a0425886d148f68a8f94716ed47a78"
	writer, err := AttemptLog(paths, attempt)
	require.NoError(t, err)
	var work sync.WaitGroup
	for index := range 50 {
		work.Go(func() {
			_, err := fmt.Fprintf(writer, "writer-%02d\n", index)
			assert.NoError(t, err)
		})
	}
	work.Wait()
	require.NoError(t, writer.Close())
	data, err := os.ReadFile(paths.AttemptLogPath(attempt))
	require.NoError(t, err)
	for index := range 50 {
		assert.Equal(t, 1, strings.Count(string(data), fmt.Sprintf("writer-%02d\n", index)))
	}
	writer, err = AttemptLog(paths, attempt)
	require.NoError(t, err)
	require.NoError(t, writer.file.Close())
	_, err = writer.WriteString("cannot persist this line")
	require.ErrorIs(t, err, os.ErrClosed)
	require.ErrorIs(t, writer.Close(), os.ErrClosed)
	_, err = writer.WriteString("already closed")
	require.ErrorIs(t, err, os.ErrClosed)
}

func TestReadExcerptUsesAvailableSegmentsAndCapsOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempt.log")
	require.NoError(t, atomicfile.WriteFileDurable(path, []byte("newest\n"), 0o600))
	require.NoError(t, atomicfile.WriteFileDurable(path+".2", []byte("older\n"), 0o600))
	excerpt, err := ReadExcerpt(path)
	require.NoError(t, err)
	assert.Equal(t, "older\nnewest", excerpt, "a missing middle segment must not discard available text")
	require.NoError(t, atomicfile.WriteFileDurable(path, []byte(strings.Repeat("x", 128<<10)+"reason\n"), 0o600))
	excerpt, err = ReadExcerpt(path)
	require.NoError(t, err)
	assert.Len(t, excerpt, maxExcerptBytes-1)
	assert.True(t, strings.HasSuffix(excerpt, "reason"))
}

// Attempt diagnostics obey the excerpt caps, survive a missing log,
// keep the previous attempt's log separate, and retire completed
// attempts down to the retention bound while the current one stays.
func TestStartupDiagnosticsAreAttemptSpecific(t *testing.T) {
	t.Run("an empty and a missing log read as empty excerpts", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, atomicfile.WriteFileDurable(filepath.Join(dir, "empty.log"), nil, 0o600))
		got, err := ReadExcerpt(filepath.Join(dir, "empty.log"))
		require.NoError(t, err)
		assert.Empty(t, got)

		got, err = ReadExcerpt(filepath.Join(dir, "absent.log"))
		require.NoError(t, err, "a missing log must not fail the primary error")
		assert.Empty(t, got)
	})

	t.Run("a huge log keeps only its limited tail", func(t *testing.T) {
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
		require.NoError(t, atomicfile.WriteFileDurable(huge, []byte(sb.String()), 0o600))

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
		require.NoError(t, atomicfile.MkdirAllPrivate(paths.Logs))
		for i := range maxRetainedAttempts + 4 {
			id := fmt.Sprintf("%032x", i)
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
		assert.Equal(t, maxRetainedAttempts+1, len(entries),
			"the current attempt plus the retained completed attempts survive")
	})
}
