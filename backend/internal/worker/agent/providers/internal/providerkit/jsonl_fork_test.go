package providerkit

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoredSessionQueryForLaunch(t *testing.T) {
	t.Parallel()

	env := []string{"FOO=bar", "CLAUDE_CONFIG_DIR=/custom/claude"}
	q := StoredSessionQueryForLaunch("/home/alice", env)
	assert.Equal(t, "/home/alice", q.Home())
	assert.Equal(t, "/custom/claude", q.Env("CLAUDE_CONFIG_DIR"))
	assert.Equal(t, "bar", q.Env("FOO"))
}

func TestStreamForkJSONL(t *testing.T) {
	t.Parallel()

	t.Run("rewrites single header line and streams body verbatim", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "src.jsonl")
		dst := filepath.Join(dir, "nested", "dst.jsonl")
		body := `{"type":"message","text":"line 1"}` + "\n" + `{"type":"message","text":"line 2"}` + "\n"
		require.NoError(t, os.WriteFile(src, []byte(`{"type":"session","id":"old"}`+"\n"+body), 0o600))

		err := StreamForkJSONL(src, dst, 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
			line, readErr := lr.ReadLine()
			if readErr != nil {
				return nil, readErr
			}
			assert.Equal(t, `{"type":"session","id":"old"}`, string(line))
			return [][]byte{[]byte(`{"type":"session","id":"new"}`)}, nil
		})
		require.NoError(t, err)

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, `{"type":"session","id":"new"}`+"\n"+body, string(got))

		if runtime.GOOS != "windows" {
			info, statErr := os.Stat(dst)
			require.NoError(t, statErr)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	})

	t.Run("handles multi-line headers larger than 64 KiB and large body lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "large.jsonl")
		dst := filepath.Join(dir, "out.jsonl")

		largeTitle := `{"type":"title","pad":"` + strings.Repeat("t", 96*1024) + `"}`
		largeHeader := `{"type":"session","id":"old","pad":"` + strings.Repeat("h", 80*1024) + `"}`
		largeBody := `{"type":"message","text":"` + strings.Repeat("b", 128*1024) + `"}` + "\n"
		require.NoError(t, os.WriteFile(src, []byte(largeTitle+"\r\n"+largeHeader+"\r\n"+largeBody), 0o600))

		err := StreamForkJSONL(src, dst, 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
			line1, err := lr.ReadLine()
			if err != nil {
				return nil, err
			}
			line2, err := lr.ReadLine()
			if err != nil {
				return nil, err
			}
			assert.Equal(t, largeTitle, string(line1))
			assert.Equal(t, largeHeader, string(line2))
			rewritten := strings.Replace(string(line2), `"id":"old"`, `"id":"new"`, 1)
			return [][]byte{line1, []byte(rewritten)}, nil
		})
		require.NoError(t, err)

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		wantHeader := strings.Replace(largeHeader, `"id":"old"`, `"id":"new"`, 1)
		assert.Equal(t, largeTitle+"\n"+wantHeader+"\n"+largeBody, string(got))
	})

	t.Run("handles single-line source without trailing newline", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "single.jsonl")
		dst := filepath.Join(dir, "dst.jsonl")
		require.NoError(t, os.WriteFile(src, []byte(`{"type":"session","id":"old"}`), 0o600))

		err := StreamForkJSONL(src, dst, 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
			line, readErr := lr.ReadLine()
			if readErr != nil {
				return nil, readErr
			}
			assert.Equal(t, `{"type":"session","id":"old"}`, string(line))
			next, nextErr := lr.ReadLine()
			assert.Nil(t, next)
			assert.ErrorIs(t, nextErr, io.EOF)
			return [][]byte{[]byte(`{"type":"session","id":"new"}`)}, nil
		})
		require.NoError(t, err)

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, `{"type":"session","id":"new"}`+"\n", string(got))
	})

	t.Run("returns io.EOF on empty file and does not create destination directory on error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "empty.jsonl")
		dstDir := filepath.Join(dir, "should-not-exist")
		dst := filepath.Join(dstDir, "dst.jsonl")
		require.NoError(t, os.WriteFile(src, nil, 0o600))

		err := StreamForkJSONL(src, dst, 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
			_, readErr := lr.ReadLine()
			if errors.Is(readErr, io.EOF) {
				return nil, errors.New("source is empty")
			}
			return nil, readErr
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "source is empty")
		assert.NoDirExists(t, dstDir)
	})

	t.Run("rejects nil callback, directory source, and symlink source", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "real.jsonl")
		require.NoError(t, os.WriteFile(src, []byte(`{"type":"session"}`+"\n"), 0o600))

		err := StreamForkJSONL(src, filepath.Join(dir, "dst.jsonl"), 0o600, nil)
		require.Error(t, err)

		err = StreamForkJSONL(dir, filepath.Join(dir, "dst.jsonl"), 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
			return nil, nil
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")

		if runtime.GOOS != "windows" {
			link := filepath.Join(dir, "link.jsonl")
			require.NoError(t, os.Symlink(src, link))
			err = StreamForkJSONL(link, filepath.Join(dir, "dst.jsonl"), 0o600, func(lr *JSONLLineReader) ([][]byte, error) {
				return nil, nil
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not a regular file")
		}
	})
}

func TestStreamCopyRegularFile(t *testing.T) {
	t.Parallel()

	t.Run("copies regular file atomically across directories", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "src.jsonl")
		dst := filepath.Join(dir, "sub", "dst.jsonl")
		payload := strings.Repeat("line content\n", 10*1024)
		require.NoError(t, os.WriteFile(src, []byte(payload), 0o600))

		require.NoError(t, StreamCopyRegularFile(src, dst, 0o600))

		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, payload, string(got))
	})

	t.Run("rejects non-regular files", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		err := StreamCopyRegularFile(dir, filepath.Join(dir, "dst.jsonl"), 0o600)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")

		if runtime.GOOS != "windows" {
			realFile := filepath.Join(dir, "real.jsonl")
			require.NoError(t, os.WriteFile(realFile, []byte("x"), 0o600))
			link := filepath.Join(dir, "link.jsonl")
			require.NoError(t, os.Symlink(realFile, link))

			err = StreamCopyRegularFile(link, filepath.Join(dir, "dst.jsonl"), 0o600)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not a regular file")
		}
	})
}
