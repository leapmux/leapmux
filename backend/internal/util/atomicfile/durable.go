package atomicfile

import "os"

// SyncDirectory confirms directory-entry changes on platforms that require a
// directory barrier. Windows uses its native publication operations instead.
func SyncDirectory(path string) error { return ops.syncDir(path) }

// WriteFileDurable writes data to path with the same all-or-nothing
// replacement WriteFile provides, and additionally reports success only
// after the platform's documented persistence operations complete: the
// file's bytes, the replacement, and the directory entry that names it.
//
// A caller that must survive power loss -- a pending registration record,
// saved credentials -- uses this rather than WriteFile. Callers whose
// worst case is a stale copy keep WriteFile.
//
// On Unix the sequence is file flush, rename, and a flush of the
// containing directory, which is what publishes the new name. On Windows
// the replacement runs through MoveFileExW with
// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH, which the system
// documents as returning only after the change reaches the disk. Neither
// platform promises more than its documentation states, and neither is
// claimed here.
func WriteFileDurable(path string, data []byte, mode os.FileMode) error {
	if err := writeFileSequenced(path, data, mode, replaceFileDurable); err != nil {
		return err
	}
	return publishFileEntry(path)
}
