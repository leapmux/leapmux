// Package canonicalpath resolves filesystem identity before durable publication.
package canonicalpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Directory resolves existing symlinks and preserves a missing directory suffix.
// Looking up path components requires no directory listing or ancestor flush.
func Directory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("a directory path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve the home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve the absolute directory path: %w", err)
	}
	return resolve(absolute)
}

func resolve(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve directory %s: %w", path, err)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	canonicalParent, err := resolve(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonicalParent, filepath.Base(path)), nil
}
