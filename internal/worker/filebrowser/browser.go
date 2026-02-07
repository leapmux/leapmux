package filebrowser

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const defaultReadLimit = 64 * 1024 // 64KB

// FileEntry represents a file or directory.
type FileEntry struct {
	Name        string
	IsDir       bool
	Size        int64
	ModTime     string
	Permissions string
}

// ListDirectory lists entries in a directory. The path must be absolute.
func ListDirectory(path string) (string, []FileEntry, error) {
	absPath, err := securePath(path)
	if err != nil {
		return "", nil, err
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
		result = append(result, FileEntry{
			Name:        e.Name(),
			IsDir:       e.IsDir(),
			Size:        info.Size(),
			ModTime:     info.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"),
			Permissions: info.Mode().String(),
		})
	}

	return absPath, result, nil
}

// ReadFile reads file content with offset and limit. The path must be absolute.
func ReadFile(path string, offset, limit int64) (string, []byte, int64, error) {
	absPath, err := securePath(path)
	if err != nil {
		return "", nil, 0, err
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
	absPath, err := securePath(path)
	if err != nil {
		return "", nil, err
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

// securePath validates and resolves a path, preventing directory traversal.
// Expands ~ and ~/ to the current user's home directory.
func securePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	// Reject paths with suspicious patterns.
	if strings.Contains(path, "\x00") {
		return "", fmt.Errorf("path contains null byte")
	}

	// Expand ~ to home directory.
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}

	// Resolve to absolute path.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Clean the path to resolve .. and .
	absPath = filepath.Clean(absPath)

	return absPath, nil
}
