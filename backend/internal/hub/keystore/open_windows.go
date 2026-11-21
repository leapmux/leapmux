//go:build windows

package keystore

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openKeyRing shares delete access with the publisher. Windows can expose
// the new path while the write-through rename still holds its delete handle.
// The publisher writes, syncs, and closes the complete ring before renaming it.
func openKeyRing(path string) (*os.File, error) {
	if path == "" {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	nativePath, err := keyRingReadPath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	name, err := windows.UTF16PtrFromString(nativePath)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// keyRingReadPath preserves the long-path support that os.Open provides.
func keyRingReadPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	const legacyPathLimit = 248 // MAX_PATH minus room for an 8.3 file name.
	if len(absolute) < legacyPathLimit || strings.HasPrefix(absolute, `\\?\`) ||
		strings.HasPrefix(absolute, `\\.\`) || strings.HasPrefix(absolute, `\??\`) {
		return path, nil
	}
	if uncPath, ok := strings.CutPrefix(absolute, `\\`); ok {
		return `\\?\UNC\` + uncPath, nil
	}
	return `\\?\` + absolute, nil
}
