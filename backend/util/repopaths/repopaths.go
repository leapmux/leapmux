// Package repopaths standardizes LeapMux repository-local directories under
// <git-toplevel>/.leapmux/<category> and ensures .leapmux/tmp/ is excluded in
// the repository's local Git exclude file (.git/info/exclude).
package repopaths

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/pathutil"
)

const (
	leapmuxDirName   = ".leapmux"
	tmpCategory      = "tmp"
	tmpExcludeEntry  = ".leapmux/tmp/"
	tmpExcludeNoSlsh = ".leapmux/tmp"
	excludeLockWait  = 10 * time.Second
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
	return gitInfoExcludePathForLocation(loc)
}

func gitInfoExcludePathForLocation(loc gitLocation) (string, bool) {
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

// EnsureTmpExcluded ensures the repository's local Git info/exclude file lists
// .leapmux/tmp/. It returns nil when dir is not inside a Git repository, and is
// idempotent when .leapmux/tmp or .leapmux/tmp/ is already present.
func EnsureTmpExcluded(dir string) error {
	excludePath, ok := GitInfoExcludePath(dir)
	if !ok {
		return nil
	}
	return ensureTmpExcludedAt(excludePath)
}

func ensureTmpExcludedAt(excludePath string) error {
	excludeMu.Lock()
	defer excludeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create git info directory %s: %w", filepath.Dir(excludePath), err)
	}

	lockPath := excludePath + ".lock"
	lockFile, err := filelock.Open(lockPath, false)
	if err != nil {
		return fmt.Errorf("open git exclude lock %s: %w", lockPath, err)
	}
	defer func() { _ = lockFile.Close() }()

	lockCtx, cancel := context.WithTimeout(context.Background(), excludeLockWait)
	defer cancel()
	if err := filelock.Lock(lockCtx, lockFile); err != nil {
		return fmt.Errorf("lock git exclude file %s: %w", excludePath, err)
	}
	defer func() { _ = filelock.Unlock(lockFile) }()

	// Preserve a symlinked info/exclude file by resolving its target before
	// atomic replacement so the symlink itself is never replaced with a regular
	// file.
	targetPath := excludePath
	if lfi, lstatErr := os.Lstat(excludePath); lstatErr == nil && lfi.Mode()&os.ModeSymlink != 0 {
		resolved, evalErr := filepath.EvalSymlinks(excludePath)
		if evalErr != nil {
			return fmt.Errorf("resolve git exclude symlink %s: %w", excludePath, evalErr)
		}
		targetPath = resolved
	} else if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		return fmt.Errorf("inspect git exclude file %s: %w", excludePath, lstatErr)
	}

	mode := os.FileMode(0o644)
	var existing []byte
	fi, statErr := os.Stat(targetPath)
	if statErr == nil {
		if fi.IsDir() {
			return fmt.Errorf("git exclude path %s is a directory", targetPath)
		}
		if perm := fi.Mode().Perm(); perm != 0 {
			mode = perm
		}
		var readErr error
		existing, readErr = os.ReadFile(targetPath)
		if readErr != nil {
			return fmt.Errorf("read git exclude file %s: %w", targetPath, readErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect git exclude file %s: %w", targetPath, statErr)
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

	if err := atomicfile.WriteFile(targetPath, []byte(b.String()), mode); err != nil {
		return fmt.Errorf("write git exclude file %s: %w", targetPath, err)
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

func containsLeapMuxTmpSegments(abs string) bool {
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i := 0; i+1 < len(parts); i++ {
		if pathSegmentEqual(parts[i], leapmuxDirName) && pathSegmentEqual(parts[i+1], tmpCategory) {
			return true
		}
	}
	return false
}

func pathSegmentEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func resolveInsideLeapMuxTmp(path string) (gitLocation, bool) {
	if strings.TrimSpace(path) == "" {
		return gitLocation{}, false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return gitLocation{}, false
	}
	abs = filepath.Clean(abs)
	// Fast path: skip walking parent directories for .git unless the cleaned
	// path actually contains the ".leapmux" and "tmp" segments in sequence.
	if !containsLeapMuxTmpSegments(abs) {
		return gitLocation{}, false
	}
	loc, ok := findGitLocation(abs)
	if !ok {
		return gitLocation{}, false
	}
	tmpDir := filepath.Join(loc.toplevel, leapmuxDirName, tmpCategory)
	if !pathutil.HasPathPrefix(abs, tmpDir) {
		return gitLocation{}, false
	}
	return loc, true
}

// IsInsideLeapMuxTmp reports whether path lies at or inside
// <git-toplevel>/.leapmux/tmp for the Git repository enclosing path.
func IsInsideLeapMuxTmp(path string) (toplevel string, ok bool) {
	loc, ok := resolveInsideLeapMuxTmp(path)
	if !ok {
		return "", false
	}
	return loc.toplevel, true
}

// EnsureTmpExcludedIfInside ensures the repository's local Git info/exclude
// file lists .leapmux/tmp/ when path lies at or inside
// <git-toplevel>/.leapmux/tmp. It inspects path segments in memory before
// walking the filesystem for .git, and resolves the Git location at most once.
func EnsureTmpExcludedIfInside(path string) error {
	loc, ok := resolveInsideLeapMuxTmp(path)
	if !ok {
		return nil
	}
	excludePath, ok := gitInfoExcludePathForLocation(loc)
	if !ok {
		return nil
	}
	return ensureTmpExcludedAt(excludePath)
}
