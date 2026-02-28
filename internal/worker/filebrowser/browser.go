package filebrowser

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/hub/validate"
)

const defaultReadLimit = 64 * 1024 // 64KB

// userHomeDir returns the current user's home directory, or "" on failure.
func userHomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// FileEntry represents a file or directory.
type FileEntry struct {
	Name        string
	IsDir       bool
	Size        int64
	ModTime     string
	Permissions string
}

// ListDirectory lists entries in a directory. The path must be absolute.
// When maxDepth > 0, single-child directory entries are merged server-side
// (e.g. "a/b/c") to reduce round-trip RPC calls from the frontend.
func ListDirectory(path string, maxDepth int) (string, []FileEntry, error) {
	absPath := validate.SanitizePath(path, userHomeDir())
	if absPath == "" {
		return "", nil, fmt.Errorf("invalid path")
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("read directory: %w", err)
	}

	result := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		entry := FileEntry{
			Name:        e.Name(),
			IsDir:       e.IsDir(),
			Size:        info.Size(),
			ModTime:     info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
			Permissions: info.Mode().String(),
		}
		if e.IsDir() && maxDepth > 0 {
			entry = mergeEntry(filepath.Join(absPath, e.Name()), entry, maxDepth)
		}
		result = append(result, entry)
	}

	return absPath, result, nil
}

// mergeEntry recursively merges single-child directories into the entry name.
// For example, if "src" contains only "components" which contains only "shell",
// the returned entry will have Name "src/components/shell".
func mergeEntry(dirPath string, entry FileEntry, remaining int) FileEntry {
	if remaining <= 0 {
		return entry
	}
	children, err := os.ReadDir(dirPath)
	if err != nil {
		return entry
	}
	if len(children) != 1 || !children[0].IsDir() {
		return entry
	}
	child := children[0]
	childInfo, err := child.Info()
	if err != nil {
		return entry
	}
	merged := FileEntry{
		Name:        entry.Name + "/" + child.Name(),
		IsDir:       true,
		Size:        childInfo.Size(),
		ModTime:     childInfo.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
		Permissions: childInfo.Mode().String(),
	}
	return mergeEntry(filepath.Join(dirPath, child.Name()), merged, remaining-1)
}

// ReadFile reads file content with offset and limit. The path must be absolute.
func ReadFile(path string, offset, limit int64) (string, []byte, int64, error) {
	absPath := validate.SanitizePath(path, userHomeDir())
	if absPath == "" {
		return "", nil, 0, fmt.Errorf("invalid path")
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", nil, 0, fmt.Errorf("stat file: %w", err)
	}

	if info.IsDir() {
		return "", nil, 0, fmt.Errorf("path is a directory")
	}

	if limit <= 0 {
		limit = defaultReadLimit
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", nil, 0, fmt.Errorf("seek: %w", err)
		}
	}

	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", nil, 0, fmt.Errorf("read: %w", err)
	}

	return absPath, buf[:n], info.Size(), nil
}

// StatFile returns information about a file or directory. The path must be absolute.
func StatFile(path string) (string, *FileEntry, error) {
	absPath := validate.SanitizePath(path, userHomeDir())
	if absPath == "" {
		return "", nil, fmt.Errorf("invalid path")
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", nil, fmt.Errorf("stat: %w", err)
	}

	return absPath, &FileEntry{
		Name:        info.Name(),
		IsDir:       info.IsDir(),
		Size:        info.Size(),
		ModTime:     info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
		Permissions: info.Mode().String(),
	}, nil
}
