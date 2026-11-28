// Package repopaths standardizes LeapMux repository-local directories under
// <git-toplevel>/.leapmux/<category> and ensures .leapmux/tmp/ is excluded in
// the repository's local Git exclude file (.git/info/exclude).
package repopaths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/pathutil"
)

const (
	leapmuxDirName   = ".leapmux"
	tmpCategory      = "tmp"
	tmpExcludeEntry  = ".leapmux/tmp/"
	tmpExcludeNoSlsh = ".leapmux/tmp"
)

var excludeMu sync.Mutex

type gitLocation struct {
	toplevel string
	gitDir   string
}

// findGitLocation walks up from dir (after filepath.Abs and filepath.Clean)
// looking for a .git directory or a regular .git file starting with "gitdir:".
func findGitLocation(dir string) (gitLocation, bool) {
	if strings.TrimSpace(dir) == "" {
		return gitLocation{}, false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return gitLocation{}, false
	}
	cur := filepath.Clean(abs)
	for {
		gitPath := filepath.Join(cur, ".git")
		fi, statErr := os.Stat(gitPath)
		if statErr == nil {
			if fi.IsDir() {
				return gitLocation{toplevel: cur, gitDir: gitPath}, true
			}
			if fi.Mode().IsRegular() {
				if gitDir, ok := parseGitDirFile(cur, gitPath); ok {
					return gitLocation{toplevel: cur, gitDir: gitDir}, true
				}
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return gitLocation{}, false
		}
		cur = parent
	}
}

func parseGitDirFile(toplevel, gitFilePath string) (string, bool) {
	raw, err := os.ReadFile(gitFilePath)
	if err != nil {
		return "", false
	}
	firstLine, _, _ := strings.Cut(string(raw), "\n")
	firstLine = strings.TrimSuffix(firstLine, "\r")
	target, ok := strings.CutPrefix(firstLine, "gitdir:")
	if !ok {
		return "", false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(toplevel, target)
	}
	return filepath.Clean(target), true
}

// GitToplevel walks up from dir looking for a .git entry (either a directory
// or a regular file starting with "gitdir:"). It returns the directory holding
// .git and true, or "", false when dir is not inside a Git repository.
func GitToplevel(dir string) (string, bool) {
	loc, ok := findGitLocation(dir)
	if !ok {
		return "", false
	}
	return loc.toplevel, true
}

// GitInfoExcludePath resolves the local Git info/exclude file path for dir.
// For linked worktrees (whose gitdir contains a commondir file), it resolves
// commondir and returns <commondir>/info/exclude; for regular repositories and
// submodules without commondir, it returns <gitdir>/info/exclude.
func GitInfoExcludePath(dir string) (string, bool) {
	loc, ok := findGitLocation(dir)
	if !ok {
		return "", false
	}
	excludeRoot := loc.gitDir
	commonDirFile := filepath.Join(loc.gitDir, "commondir")
	raw, err := os.ReadFile(commonDirFile)
	if err == nil {
		common := strings.TrimSpace(string(raw))
		if common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(loc.gitDir, common)
			}
			excludeRoot = filepath.Clean(common)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	return filepath.Join(excludeRoot, "info", "exclude"), true
}

// CategoryDir returns <git-toplevel>/.leapmux/<category> when dir is inside a
// Git repository, or <dir>/.leapmux/<category> otherwise.
func CategoryDir(dir, category string) string {
	if toplevel, ok := GitToplevel(dir); ok {
		return filepath.Join(toplevel, leapmuxDirName, category)
	}
	return filepath.Join(dir, leapmuxDirName, category)
}

// EnsureTmpExcluded ensures the repository's local Git info/exclude file lists
// .leapmux/tmp/. It returns nil when dir is not inside a Git repository, and is
// idempotent when .leapmux/tmp or .leapmux/tmp/ is already present.
func EnsureTmpExcluded(dir string) error {
	excludePath, ok := GitInfoExcludePath(dir)
	if !ok {
		return nil
	}

	excludeMu.Lock()
	defer excludeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git info directory %s: %w", filepath.Dir(excludePath), err)
	}

	mode := os.FileMode(0o644)
	var existing []byte
	fi, statErr := os.Stat(excludePath)
	if statErr == nil {
		if fi.IsDir() {
			return fmt.Errorf("git exclude path %s is a directory", excludePath)
		}
		if perm := fi.Mode().Perm(); perm != 0 {
			mode = perm
		}
		var readErr error
		existing, readErr = os.ReadFile(excludePath)
		if readErr != nil {
			return fmt.Errorf("read git exclude file %s: %w", excludePath, readErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect git exclude file %s: %w", excludePath, statErr)
	}

	if hasTmpExcludeEntry(existing) {
		return nil
	}

	var b strings.Builder
	b.Grow(len(existing) + len(tmpExcludeEntry) + 2)
	b.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString(tmpExcludeEntry)
	b.WriteByte('\n')

	if err := atomicfile.WriteFile(excludePath, []byte(b.String()), mode); err != nil {
		return fmt.Errorf("write git exclude file %s: %w", excludePath, err)
	}
	return nil
}

func hasTmpExcludeEntry(raw []byte) bool {
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == tmpExcludeNoSlsh || trimmed == tmpExcludeEntry {
			return true
		}
	}
	return false
}

// EnsureTmpDir creates <base>/.leapmux/tmp (resolved via CategoryDir) and
// excludes .leapmux/tmp/ in the repository's local Git exclude file when inside
// a Git repository.
func EnsureTmpDir(dir string) (string, error) {
	tmpDir := CategoryDir(dir, tmpCategory)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create leapmux tmp directory %s: %w", tmpDir, err)
	}
	if err := EnsureTmpExcluded(dir); err != nil {
		return tmpDir, err
	}
	return tmpDir, nil
}

// IsInsideLeapMuxTmp reports whether path lies at or inside
// <git-toplevel>/.leapmux/tmp for the Git repository enclosing path.
func IsInsideLeapMuxTmp(path string) (toplevel string, ok bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	toplevel, ok = GitToplevel(abs)
	if !ok {
		return "", false
	}
	tmpDir := filepath.Join(toplevel, leapmuxDirName, tmpCategory)
	if !pathutil.HasPathPrefix(abs, tmpDir) {
		return "", false
	}
	return toplevel, true
}
