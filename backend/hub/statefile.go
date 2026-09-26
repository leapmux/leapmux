package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

// stateFileName is the file the hub writes in its data directory, beside the
// database: the process id and the addresses it actually serves.
//
// It is how a client discovers an address the operator did not name -- the
// port the operating system chose for a `--listen :0` request -- and how a
// reader tells a live hub from a crashed one: a crash leaves the file behind
// with a pid that answers nothing, and the next start overwrites it.
const stateFileName = "state.json"

// hubState is the whole file. Two fields, because those two are what a
// reader needs and nothing else has earned a place yet.
type hubState struct {
	PID    int      `json:"pid"`
	Listen []string `json:"listen"`
}

// stateFilePath is where the state file lives for a hub on dataDir.
func stateFilePath(dataDir string) string {
	return filepath.Join(dataDir, stateFileName)
}

// writeStateFile writes <data-dir>/state.json atomically and returns the path
// it wrote. Call it only after every listener is bound and its address is
// resolved, so the file never claims an address the hub does not answer on.
//
// Atomic through atomicfile.WriteFile: a reader sees the old content or the
// new one and never a half-written file, and a failed write leaves an earlier
// state file untouched. The pid is stamped here, from this process, and is
// the first key in the file.
func writeStateFile(dataDir string, listen []string) (string, error) {
	payload, err := json.Marshal(hubState{PID: os.Getpid(), Listen: listen})
	if err != nil {
		return "", fmt.Errorf("marshal state file: %w", err)
	}
	path := stateFilePath(dataDir)
	if err := atomicfile.WriteFile(path, payload, 0o600); err != nil {
		return "", fmt.Errorf("write state file: %w", err)
	}
	return path, nil
}

// removeStateFile deletes the state file THIS process wrote. The path comes
// from writeStateFile; a path from a write that never happened is empty, and
// removes nothing -- a failed start beside a live hub sharing the data
// directory must not delete the file that hub wrote.
//
// A missing file is not an error: a crash may have taken the file with it, or
// an operator may have cleaned the directory while the hub ran. The sweep of
// atomicfile temp files goes with the delete: an interrupted write leaves a
// copy of the same content beside the target, and a delete that misses it
// reports the content gone while it stays on the disk.
func removeStateFile(path string) error {
	if path == "" {
		return nil
	}
	err := errors.Join(os.Remove(path), atomicfile.RemoveTempFiles(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
