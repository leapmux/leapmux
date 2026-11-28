package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	rootTempSuffixBytes = 8
	maxRootTempAttempts = 100
)

// WriteFileInRoot writes data to rel inside root atomically, confining both
// the temporary file and the replacement to root through kernel-enforced
// directory handles.
//
// rel must be a local relative file path under root. WriteFileInRoot rejects
// a nil root, ".", parent traversal, absolute paths, and trailing path
// separators. The destination's parent directory must already exist inside
// root (matching WriteFile and os.WriteFile).
//
// On every failure path before replacement, WriteFileInRoot closes and removes
// the temporary file inside root so no partial or leftover copy remains.
func WriteFileInRoot(root *os.Root, rel string, data []byte, mode os.FileMode) error {
	if root == nil {
		return errors.New("atomicfile: root is nil")
	}
	cleanRel, err := validateRootRelPath(rel)
	if err != nil {
		return err
	}
	f, tmpRel, err := createTempInRoot(root, cleanRel)
	if err != nil {
		return err
	}
	return finishTemporaryWrite(f, data, mode,
		func() error { return replaceFileInRoot(root, tmpRel, cleanRel) },
		func() error { return root.Remove(tmpRel) },
	)
}

func validateRootRelPath(rel string) (string, error) {
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("atomicfile: path %q is not local to root", rel)
	}
	if os.IsPathSeparator(rel[len(rel)-1]) {
		return "", fmt.Errorf("atomicfile: path %q must refer to a file", rel)
	}
	if filepath.Base(rel) == "." {
		return "", fmt.Errorf("atomicfile: path %q must refer to a file", rel)
	}
	// Reject parent-traversal segments such as "a/../b" before cleaning so
	// lexical normalization cannot bypass a missing or symlinked directory.
	for _, seg := range strings.FieldsFunc(rel, isPathSepRune) {
		if seg == ".." {
			return "", fmt.Errorf("atomicfile: path %q must not contain parent traversal", rel)
		}
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." {
		return "", fmt.Errorf("atomicfile: path %q must refer to a file", rel)
	}
	return clean, nil
}

func isPathSepRune(r rune) bool {
	return r < 128 && os.IsPathSeparator(byte(r))
}

func createTempInRoot(root *os.Root, cleanRel string) (*os.File, string, error) {
	parent := filepath.Dir(cleanRel)
	base := filepath.Base(cleanRel)
	var lastErr error
	for range maxRootTempAttempts {
		suffix, err := randomHex(rootTempSuffixBytes)
		if err != nil {
			return nil, "", err
		}
		tmpName := base + tempSuffix + suffix
		tmpRel := tmpName
		if parent != "." {
			tmpRel = filepath.Join(parent, tmpName)
		}
		f, err := root.OpenFile(tmpRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, tmpRel, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
		lastErr = err
	}
	return nil, "", fmt.Errorf("atomicfile: create temporary file beside %q: %w", cleanRel, lastErr)
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
