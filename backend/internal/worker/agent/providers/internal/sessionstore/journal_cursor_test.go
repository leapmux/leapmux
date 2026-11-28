package sessionstore

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJournalCursorBytesAndReaderAgree(t *testing.T) {
	t.Parallel()

	initial := []byte("line-1\nline-2\npartial-without-newline")
	fromBytes := NewJournalCursor(initial)
	fromReader, total, err := ReadJournalCursor(bytes.NewReader(initial), int64(len(initial)))
	require.NoError(t, err)
	assert.Equal(t, int64(len(initial)), total)
	assert.Equal(t, fromBytes, fromReader)
	assert.Equal(t, int64(len("line-1\nline-2\n")), fromBytes.Size())

	updated := []byte("line-1\nline-2\nline-3\nline-4\ntrailing")
	appended, ok := fromBytes.AppendedBytes(updated)
	require.True(t, ok)
	assert.Equal(t, []byte("line-3\nline-4\n"), appended)

	// VerifyPrefix advances the reader to the exact end of the verified prefix.
	reader := bytes.NewReader(updated)
	matched, err := fromReader.VerifyPrefix(reader)
	require.NoError(t, err)
	require.True(t, matched)
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, []byte("line-3\nline-4\ntrailing"), rest)
}

func TestJournalCursorDetectsRewriteTruncationAndLimit(t *testing.T) {
	t.Parallel()

	cursor := NewJournalCursor([]byte("alpha\nbeta\n"))

	// Modified prefix fails both byte and reader checks.
	rewritten := []byte("ALPHA\nbeta\ngamma\n")
	appended, ok := cursor.AppendedBytes(rewritten)
	assert.False(t, ok)
	assert.Nil(t, appended)
	matched, err := cursor.VerifyPrefix(bytes.NewReader(rewritten))
	require.NoError(t, err)
	assert.False(t, matched)

	// Truncated prefix returns (false, nil).
	truncated := []byte("alpha\n")
	_, ok = cursor.AppendedBytes(truncated)
	assert.False(t, ok)
	matched, err = cursor.VerifyPrefix(bytes.NewReader(truncated))
	require.NoError(t, err)
	assert.False(t, matched)

	// Stream exceeding maxSize returns ErrArchiveFileModeOrSize.
	_, _, err = ReadJournalCursor(strings.NewReader("0123456789\n"), 5)
	assert.ErrorIs(t, err, ErrArchiveFileModeOrSize)

	// Empty journal has size 0 and matches any prefix.
	empty := NewJournalCursor(nil)
	assert.Zero(t, empty.Size())
	appended, ok = empty.AppendedBytes([]byte("first\n"))
	require.True(t, ok)
	assert.Equal(t, []byte("first\n"), appended)
	matched, err = empty.VerifyPrefix(bytes.NewReader([]byte("first\n")))
	require.NoError(t, err)
	assert.True(t, matched)
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestJournalCursorPropagatesReadErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("disk read failed")
	_, _, err := ReadJournalCursor(failingReader{err: boom}, 1024)
	assert.ErrorIs(t, err, boom)

	cursor := NewJournalCursor([]byte("alpha\n"))
	matched, err := cursor.VerifyPrefix(failingReader{err: boom})
	assert.False(t, matched)
	assert.ErrorIs(t, err, boom)
}
