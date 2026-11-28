// Package atomicfile provides the standard "write tmp + rename" idiom
// for atomic file replacement. Callers that need an all-or-nothing
// update of a small file (credentials, pin sets, archives) should use
// WriteFile so the same failure semantics apply everywhere.
package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// tempSuffix separates the destination name from the unique part of a
// temporary file's name. WriteFile composes the name and RemoveTempFiles
// recognizes it, so the two agree by construction.
const tempSuffix = ".tmp"

// fileOps is the filesystem seam every writer in this package goes
// through. It exists so a test can observe the REAL operation order of a
// write -- create, chmod, write, sync, close, rename, and (for the
// durable writers) the parent-directory sync -- rather than infer it from
// a mocked return value. defaultFileOps performs the operations; a test
// wraps it and records.
//
// The operations are one-to-one with the calls WriteFile already made;
// introducing the seam changed their order nowhere.
type fileOps interface {
	createTemp(dir, pattern string, mode os.FileMode) (*os.File, error)
	chmod(f *os.File, mode os.FileMode) error
	write(f *os.File, data []byte) error
	sync(f *os.File) error
	close(f *os.File) error
	rename(oldpath, newpath string) error
	remove(path string) error
	// syncDir flushes a directory entry. No writer below calls it until
	// WriteFileDurable; it is on the seam so the same observation point
	// covers the publication step.
	syncDir(path string) error
}

// defaultFileOps performs the production filesystem operations.
type defaultFileOps struct{}

func (defaultFileOps) createTemp(dir, pattern string, mode os.FileMode) (*os.File, error) {
	return createTemporaryFile(dir, pattern, mode)
}

func (defaultFileOps) chmod(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }

func (defaultFileOps) write(f *os.File, data []byte) error { _, err := f.Write(data); return err }

func (defaultFileOps) sync(f *os.File) error { return f.Sync() }

func (defaultFileOps) close(f *os.File) error { return f.Close() }

func (defaultFileOps) rename(oldpath, newpath string) error { return replaceFile(oldpath, newpath) }

func (defaultFileOps) remove(path string) error { return os.Remove(path) }

// ops is the seam the package's writers use. Package-private on purpose:
// only this package's writers and its tests substitute it.
var ops fileOps = defaultFileOps{}

// WriteFile writes data to path atomically: it first writes the bytes
// to a UNIQUE temporary file beside path with mode, then replaces path
// with that file. On the success path the replace is the only observable
// mutation, so a crash partway through cannot leave path truncated or
// partially overwritten. On the failure path WriteFile removes the
// temporary file so the next attempt starts from a clean state.
//
// The name is unique per call, and that is what makes the write atomic
// between PROCESSES as well as within one. A fixed "<path>.tmp" comes
// from the destination alone, so two processes that write the same file
// at the same instant open the same temporary file, interleave their
// bytes, and rename a mixed document onto path -- after which every
// reader reports a parse failure. The credential file takes exactly that
// shape, because every long-lived command rotates its token by itself.
//
// On Windows the replace is not one kernel call: a destination that
// still has an open handle fails with ACCESS_DENIED, so replaceFile
// retries a sharing conflict. It does not retry a directory or a
// read-only destination; those cannot succeed.
//
// The bytes reach the disk before the rename, so a machine that loses
// power right after the rename cannot replace the previous content
// with an empty file.
//
// The destination directory must already exist (matching os.WriteFile);
// callers responsible for first-time setup should call os.MkdirAll on
// the parent themselves.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return writeFileSequenced(path, data, mode, ops.rename)
}

// WriteStream writes streamed bytes to path atomically: it creates a unique
// private temporary file beside path, applies mode, calls write with the
// temporary file, syncs and closes the file, and replaces path via replaceFile.
// On any error (including an error from write), WriteStream closes and removes
// the temporary file, leaving path untouched.
//
// Use WriteStream instead of WriteFile when the payload can be large (such as a
// forked transcript) and should stream without buffering the full file in RAM.
func WriteStream(path string, mode os.FileMode, write func(io.Writer) error) error {
	if write == nil {
		return errors.New("atomicfile: write callback is nil")
	}
	return writeStreamSequenced(path, mode, func(f *os.File) error {
		return write(f)
	}, ops.rename)
}

// writeFileSequenced is the one implementation of the temporary-file
// write sequence, shared by WriteFile and WriteFileDurable. replace is
// the publication step: the atomic writer passes the platform replace,
// the durable one passes its write-through variant. The operations run
// through the package's seam in one order for both.
func writeFileSequenced(path string, data []byte, mode os.FileMode, replace func(oldpath, newpath string) error) error {
	return writeStreamSequenced(path, mode, func(f *os.File) error {
		return ops.write(f, data)
	}, replace)
}

func writeStreamSequenced(path string, mode os.FileMode, writeFn func(*os.File) error, replace func(oldpath, newpath string) error) error {
	f, err := ops.createTemp(filepath.Dir(path), filepath.Base(path)+tempSuffix, mode)
	if err != nil {
		return err
	}
	tmp := f.Name()
	return finishTemporaryStream(f, mode, writeFn,
		func() error { return replace(tmp, path) },
		func() error { return ops.remove(tmp) },
	)
}

// finishTemporaryWrite completes an atomic write against an already-opened
// temporary file. It applies mode, writes data, syncs, closes, and commits
// through publish. On any failure before commit it closes f (unless close was
// already attempted) and deletes the temporary file through removeTmp, joining
// every cleanup error except os.ErrNotExist into the returned error.
func finishTemporaryWrite(
	f *os.File,
	data []byte,
	mode os.FileMode,
	publish func() error,
	removeTmp func() error,
) error {
	return finishTemporaryStream(f, mode, func(f *os.File) error {
		return ops.write(f, data)
	}, publish, removeTmp)
}

// finishTemporaryStream completes an atomic write against an already-opened
// temporary file using writeFn to populate the file before syncing, closing,
// and committing through publish.
func finishTemporaryStream(
	f *os.File,
	mode os.FileMode,
	writeFn func(*os.File) error,
	publish func() error,
	removeTmp func() error,
) (err error) {
	// One cleanup for EVERY failure path below, including the ones that
	// return before the writer closes the file. A leftover temporary file
	// holds the same secret the destination holds, at the same mode,
	// under a name no listing of the destination shows.
	committed := false
	closeAttempted := false
	defer func() {
		if committed {
			return
		}
		if !closeAttempted {
			err = errors.Join(err, ops.close(f))
		}
		if removeErr := removeTmp(); !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()

	// Temporary files start at 0600, so the writer applies a mode that must
	// be wider (or narrower) here rather than after the rename, where a
	// reader could see the file at the wrong mode.
	if err := ops.chmod(f, mode); err != nil {
		return err
	}
	if err := writeFn(f); err != nil {
		return err
	}
	if err := ops.sync(f); err != nil {
		return err
	}
	closeAttempted = true
	if err := ops.close(f); err != nil {
		return err
	}
	if err := publish(); err != nil {
		return err
	}
	committed = true
	return nil
}

// RemoveTempFiles deletes every temporary file that an interrupted
// WriteFile left beside path.
//
// WriteFile removes its own temporary file on each failure path it can
// reach, so what remains here is what a crash or a kill left: a file
// that holds the full content of the write, at the destination's mode,
// under a name that no listing of the destination shows. A caller that
// deletes path must call this too, or it reports the content gone while
// a copy of it stays on the disk.
//
// Every matching entry is attempted even when one removal fails, and
// every independent failure is joined into the result: a caller that
// could see only the first error could not name the copies that stayed
// behind, and the retry would not know what to look for.
//
// A missing directory is not a failure: there is nothing to remove.
func RemoveTempFiles(path string) error {
	files, err := TempFiles(path)
	if err != nil {
		return err
	}
	var errs []error
	for _, file := range files {
		if err := ops.remove(file); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// TempFiles lists interrupted temporary copies beside path. It uses literal
// entry prefixes, so path characters never become a glob expression.
func TempFiles(path string) ([]string, error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	prefix := base + tempSuffix
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return files, nil
}
