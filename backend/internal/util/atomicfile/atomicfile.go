// Package atomicfile provides the standard "write tmp + rename" idiom
// for atomic file replacement. Callers that need an all-or-nothing
// update of a small file (credentials, pin sets, archives) should use
// WriteFile so the same failure semantics apply everywhere.
package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
)

// tempSuffix separates the destination name from the unique part of a
// temporary file's name. WriteFile composes the name and RemoveTempFiles
// recognizes it, so the two agree by construction.
const tempSuffix = ".tmp"

// WriteFile writes data to path atomically: it first writes the bytes
// to a UNIQUE temporary file beside path with mode, then renames that
// file onto path. On the success path the rename is the only observable
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
// The bytes reach the disk before the rename, so a machine that loses
// power right after the rename cannot replace the previous content
// with an empty file.
//
// The destination directory must already exist (matching os.WriteFile);
// callers responsible for first-time setup should call os.MkdirAll on
// the parent themselves.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+tempSuffix)
	if err != nil {
		return err
	}
	tmp := f.Name()
	// One cleanup for EVERY failure path below, including the ones that
	// return before WriteFile closes the file. A leftover temporary file
	// holds the same secret the destination holds, at the same mode,
	// under a name no listing of the destination shows.
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = f.Close()
		_ = os.Remove(tmp)
	}()

	// os.CreateTemp always creates at 0600, so WriteFile applies a mode
	// that must be wider (or narrower) here rather than after the rename,
	// where a reader could see the file at the wrong mode.
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
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
// A missing directory is not a failure: there is nothing to remove.
func RemoveTempFiles(path string) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	prefix := base + tempSuffix
	var firstErr error
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		// This loop attempts every entry and reports the FIRST failure: a
		// file this process cannot remove must not hide the ones it can.
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
