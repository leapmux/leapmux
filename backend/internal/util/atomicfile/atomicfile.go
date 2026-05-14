// Package atomicfile provides the standard "write tmp + rename" idiom
// for atomic file replacement. Callers that need an all-or-nothing
// update of a small file (credentials, pin sets, archives) should use
// WriteFile so the same failure semantics apply everywhere.
package atomicfile

import "os"

// WriteFile writes data to path atomically: the bytes are first written
// to "<path>.tmp" with mode, then renamed onto path. On the success
// path the rename is the only observable mutation, so a crash partway
// through cannot leave path truncated or partially overwritten. On the
// failure path the temporary file is removed so the next attempt
// starts from a clean state.
//
// The destination directory must already exist (matching os.WriteFile);
// callers responsible for first-time setup should os.MkdirAll the
// parent themselves.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
