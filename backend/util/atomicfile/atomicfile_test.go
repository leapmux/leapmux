package atomicfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFile_WritesTheContentAtTheRequestedMode(t *testing.T) {

	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, WriteFile(path, []byte(`{"a":1}`), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(data))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
			"a credential file must not be readable by another account")
	}
}

// TestWriteFile_LeavesNoTemporaryFileBehind pins the ordinary path: the
// rename is the only file that remains.
func TestWriteFile_LeavesNoTemporaryFileBehind(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, WriteFile(path, []byte("one"), 0o600))
	require.NoError(t, WriteFile(path, []byte("two"), 0o600))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "creds.json", entries[0].Name())
}

// TestWriteFile_NeverUsesTheNameDerivedFromTheDestination is the property
// the whole atomicity claim rests on, and the one this test can prove
// deterministically.
//
// A temporary name of "<path>.tmp" comes from the destination alone,
// so two processes that write the same file at the same instant open the
// SAME temporary file, interleave their bytes into it, and rename a mixed
// document onto the destination -- after which every reader reports a parse
// failure. Occupying that one name is what makes the difference visible: a
// writer that composed it would fail here.
func TestWriteFile_NeverUsesTheNameDerivedFromTheDestination(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.Mkdir(path+".tmp", 0o700))

	require.NoError(t, WriteFile(path, []byte("one"), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))
}

// TestWriteFile_ConcurrentWritersEachLandAWholeDocument exercises the same
// property under real contention. It cannot fail on demand -- an interleave
// needs a write that the kernel splits -- so read it as a smoke test of the
// unique name, not as the proof; the test above is the proof.
//
// Windows cannot replace a destination that still has an open handle, so
// two writers of the same path get ACCESS_DENIED. replaceFile retries
// that conflict; this case is what proves the retry lands a whole
// document rather than leaving a sharing error for the caller.
func TestWriteFile_ConcurrentWritersEachLandAWholeDocument(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	first := strings.Repeat("a", 64*1024)
	second := strings.Repeat("b", 64*1024)

	var wg sync.WaitGroup
	for _, payload := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				assert.NoError(t, WriteFile(path, []byte(payload), 0o600))
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)
	assert.True(t, got == first || got == second,
		"the destination must hold exactly one writer's document, never a mixture")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "every temporary file must be gone")
}

// TestWriteFile_ReadOnlyDestination pins the permanent Windows failure
// that replaceFile must not retry: a sharing conflict is transient, a
// read-only destination is not. Unix rename replaces a 0400 file when
// the directory is writable.
func TestWriteFile_ReadOnlyDestination(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, WriteFile(path, []byte("one"), 0o600))
	require.NoError(t, os.Chmod(path, 0o400))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	err := WriteFile(path, []byte("two"), 0o600)
	if runtime.GOOS == "windows" {
		require.Error(t, err, "Windows cannot replace a read-only file")
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, "one", string(data))
	} else {
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "two", string(data))
	}

	entries, listErr := os.ReadDir(dir)
	require.NoError(t, listErr)
	require.Len(t, entries, 1, "a failed or successful replace must not leave a temporary file")
	assert.Equal(t, "creds.json", entries[0].Name())
}

// TestWriteFile_RemovesTheTemporaryFileWhenTheRenameFails pins the failure
// path. The temporary file holds the same secret the destination holds, so
// a write that could not finish must not leave it on the disk.
func TestWriteFile_RemovesTheTemporaryFileWhenTheRenameFails(t *testing.T) {

	dir := t.TempDir()
	// A DIRECTORY at the destination: the write succeeds and the rename
	// cannot replace it.
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.Mkdir(path, 0o700))

	require.Error(t, WriteFile(path, []byte("secret"), 0o600))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "creds.json", entries[0].Name(), "no temporary file may survive a failed write")
}

func TestWriteFile_ReportsAMissingDirectory(t *testing.T) {

	path := filepath.Join(t.TempDir(), "absent", "creds.json")
	assert.Error(t, WriteFile(path, []byte("x"), 0o600),
		"WriteFile matches os.WriteFile: the caller creates the directory")
}

// TestRemoveTempFiles_SweepsWhatACrashLeft is the case a caller cannot
// reach through WriteFile, because WriteFile cleans up after itself: a
// process killed between the write and the rename leaves a temporary file
// that holds a live secret under a name no listing of the destination
// shows.
func TestRemoveTempFiles_SweepsWhatACrashLeft(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.WriteFile(path, []byte("live"), 0o600))
	require.NoError(t, os.WriteFile(path+".tmp1234", []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(path+".tmp5678", []byte("secret"), 0o600))
	// A different destination's leftovers, and an unrelated file: neither
	// belongs to this path.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.json.tmp99"), []byte("other"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600))

	require.NoError(t, RemoveTempFiles(path))

	var names []string
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"creds.json", "other.json.tmp99", "notes.txt"}, names,
		"only this destination's leftovers may be removed")
}

func TestRemoveTempFiles_AcceptsAMissingDirectory(t *testing.T) {

	assert.NoError(t, RemoveTempFiles(filepath.Join(t.TempDir(), "absent", "creds.json")))
}

func TestRemoveTempFiles_AcceptsADirectoryWithNothingToRemove(t *testing.T) {

	assert.NoError(t, RemoveTempFiles(filepath.Join(t.TempDir(), "creds.json")))
}

// recordingOps records each operation it forwards to the real seam, so a
// test observes the write path's actual order rather than inferring it.
type recordingOps struct {
	defaultFileOps
	calls []string
}

func (r *recordingOps) createTemp(dir, pattern string, mode os.FileMode) (*os.File, error) {
	r.calls = append(r.calls, "createTemp")
	return r.defaultFileOps.createTemp(dir, pattern, mode)
}

func (r *recordingOps) chmod(f *os.File, mode os.FileMode) error {
	r.calls = append(r.calls, "chmod")
	return r.defaultFileOps.chmod(f, mode)
}

func (r *recordingOps) write(f *os.File, data []byte) error {
	r.calls = append(r.calls, "write")
	return r.defaultFileOps.write(f, data)
}

func (r *recordingOps) sync(f *os.File) error {
	r.calls = append(r.calls, "sync")
	return r.defaultFileOps.sync(f)
}

func (r *recordingOps) close(f *os.File) error {
	r.calls = append(r.calls, "close")
	return r.defaultFileOps.close(f)
}

func (r *recordingOps) rename(oldpath, newpath string) error {
	r.calls = append(r.calls, "rename")
	return r.defaultFileOps.rename(oldpath, newpath)
}

func (r *recordingOps) remove(path string) error {
	r.calls = append(r.calls, "remove")
	return r.defaultFileOps.remove(path)
}

func (r *recordingOps) syncDir(path string) error {
	r.calls = append(r.calls, "syncDir:"+path)
	return r.defaultFileOps.syncDir(path)
}

// TestWriteFilePublicationOrder observes the real write paths through
// the package's operations seam. The atomic write stops at the rename;
// the durable write orders file sync, rename, and parent-directory sync
// exactly that way, so a machine that loses power right after the rename
// cannot lose the new name even though its bytes reached the disk.
func TestWriteFilePublicationOrder(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")

	rec := &recordingOps{}
	withOps(t, rec)

	require.NoError(t, WriteFile(path, []byte("x"), 0o600))

	// The atomic-only contract: this exact order, and no publication
	// after the rename.
	assert.Equal(t, []string{"createTemp", "chmod", "write", "sync", "close", "rename"},
		rec.calls, "the atomic write sequence must keep its order and stop at the rename")

	rec.calls = nil
	observeDurableReplacement(t, func() { rec.calls = append(rec.calls, "rename") })
	require.NoError(t, WriteFileDurable(path, []byte("y"), 0o600))

	// The durable contract: the same sequence, then the parent
	// directory's publication.
	if runtime.GOOS != "windows" {
		assert.Equal(t,
			[]string{"createTemp", "chmod", "write", "sync", "close", "rename", "syncDir:" + dir},
			rec.calls, "the durable write must publish the renamed file's directory")
	} else {
		assert.Equal(t, []string{"createTemp", "chmod", "write", "sync", "close", "rename"},
			rec.calls, "on Windows the write-through replacement carries the publication")
	}
}

func TestWriteStream_WritesContentAtRequestedMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	err := WriteStream(path, 0o600, func(w io.Writer) error {
		_, writeErr := io.WriteString(w, `{"type":"session"}`+"\n")
		return writeErr
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"type":"session"}`+"\n", string(data))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "session.jsonl", entries[0].Name())
}

func TestWriteStream_OverwritesExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	require.NoError(t, WriteFile(path, []byte("original content"), 0o600))

	err := WriteStream(path, 0o640, func(w io.Writer) error {
		_, writeErr := io.WriteString(w, "replacement content")
		return writeErr
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "replacement content", string(data))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "session.jsonl", entries[0].Name())
}

func TestWriteStream_CallbackErrorLeavesTargetUntouchedAndRemovesTempFile(t *testing.T) {
	t.Parallel()

	t.Run("existing target stays intact", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		require.NoError(t, WriteFile(path, []byte("keep me"), 0o600))

		boom := errors.New("stream aborted mid-write")
		err := WriteStream(path, 0o600, func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial garbage")
			return boom
		})
		require.ErrorIs(t, err, boom)

		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, "keep me", string(data))

		entries, listErr := os.ReadDir(dir)
		require.NoError(t, listErr)
		require.Len(t, entries, 1)
		assert.Equal(t, "session.jsonl", entries[0].Name())
	})

	t.Run("new target is not created", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "new.jsonl")

		boom := errors.New("write failed")
		err := WriteStream(path, 0o600, func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial")
			return boom
		})
		require.ErrorIs(t, err, boom)

		_, statErr := os.Stat(path)
		assert.ErrorIs(t, statErr, os.ErrNotExist)

		entries, listErr := os.ReadDir(dir)
		require.NoError(t, listErr)
		assert.Empty(t, entries, "failed WriteStream must leave no temporary file")
	})
}

func TestWriteStream_EmptyWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	err := WriteStream(path, 0o600, func(io.Writer) error {
		return nil
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, data)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "empty.jsonl", entries[0].Name())
}

func TestWriteStream_MultiChunkStreamingWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "large.jsonl")

	var expected bytes.Buffer
	chunks := []string{
		strings.Repeat("a", 40*1024) + "\n",
		strings.Repeat("b", 75*1024) + "\n",
		strings.Repeat("c", 19*1024) + "\n",
	}
	for _, chunk := range chunks {
		expected.WriteString(chunk)
	}

	err := WriteStream(path, 0o600, func(w io.Writer) error {
		for _, chunk := range chunks {
			if _, err := io.Copy(w, strings.NewReader(chunk)); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected.Bytes(), data)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "large.jsonl", entries[0].Name())
}

func TestWriteStream_RejectsNilCallbackAndMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	err := WriteStream(filepath.Join(dir, "x.jsonl"), 0o600, nil)
	require.Error(t, err)

	entries, listErr := os.ReadDir(dir)
	require.NoError(t, listErr)
	assert.Empty(t, entries)

	missing := filepath.Join(dir, "absent", "x.jsonl")
	err = WriteStream(missing, 0o600, func(w io.Writer) error {
		_, writeErr := io.WriteString(w, "x")
		return writeErr
	})
	assert.Error(t, err)
}

func TestWriteStream_RemovesTempFileWhenRenameFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(path, 0o700))

	err := WriteStream(path, 0o600, func(w io.Writer) error {
		_, writeErr := io.WriteString(w, "secret")
		return writeErr
	})
	require.Error(t, err)

	entries, listErr := os.ReadDir(dir)
	require.NoError(t, listErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "target", entries[0].Name())
}
