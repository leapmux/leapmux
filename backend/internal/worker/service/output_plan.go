package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// archiveTimestampLayout is the compact RFC3339 format used in archived plan
// filenames. Lexicographic sort matches chronological order; no ':' so the
// names work on Windows; millisecond precision avoids collisions on chatty
// rewrites within the same second.
const archiveTimestampLayout = "20060102T150405.000Z"

// planArchiveCollisionRetries bounds the suffix counter used when two
// archives target the same millisecond stamp.
const planArchiveCollisionRetries = 1000

// resolvePlanDir returns the directory a plan file should live in.
//
// First-write-wins: when the agent has no prior plan file, the directory is
// derived from `now` (`<root>/plans/<YYYY>/<MM>`). On subsequent writes the
// existing plan stays in its original month directory — so a plan started in
// March and updated in April keeps both versions in `plans/2026/03/`, instead
// of fragmenting across months.
func (h *OutputHandler) resolvePlanDir(priorPath string, now time.Time) (string, error) {
	if priorPath != "" {
		return filepath.Dir(priorPath), nil
	}
	rootDir, err := filepath.Abs(h.DataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w", err)
	}
	return filepath.Join(rootDir, "plans", fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%02d", int(now.Month()))), nil
}

// planFilename returns `<sanitized_title>.<agent_id>.md`. The agent id makes
// the canonical filename collision-free across agents that pick the same
// plan title, so two agents in the same month never clobber each other's
// canonical paths.
func planFilename(title, agentID string) string {
	return agent.SanitizePlanFilenameTitle(title) + "." + agentID + ".md"
}

// archivePlanFile renames `currentPath` to `<base>.<timestamp>.md` (preserving
// the original sanitized-title + agent-id stem) so the new content can take
// the canonical path. Returns the archive path on success. Errors are logged
// by the caller; we never want a failed archive to block writing the new
// plan, since the new content is the user's intent.
func (h *OutputHandler) archivePlanFile(currentPath string, now time.Time) (string, error) {
	if currentPath == "" {
		return "", nil
	}
	if _, err := os.Stat(currentPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stat plan file: %w", err)
	}

	dir, file := filepath.Split(currentPath)
	stem := strings.TrimSuffix(file, ".md")
	if stem == file {
		// Defensive: if for some reason the file does not end in .md, fall
		// back to using the full name as the stem so the archive still
		// gets a unique suffix.
		stem = file
	}

	stamp := now.UTC().Format(archiveTimestampLayout)
	for counter := 0; counter <= planArchiveCollisionRetries; counter++ {
		var archivePath string
		if counter == 0 {
			archivePath = filepath.Join(dir, stem+"."+stamp+".md")
		} else {
			archivePath = filepath.Join(dir, fmt.Sprintf("%s.%s.%d.md", stem, stamp, counter))
		}
		if _, err := os.Stat(archivePath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat archive target: %w", err)
		}
		if err := os.Rename(currentPath, archivePath); err != nil {
			return "", fmt.Errorf("archive plan file: %w", err)
		}
		return archivePath, nil
	}
	return "", fmt.Errorf("too many archive collisions for %q", currentPath)
}

// writePlanFile writes content to canonicalPath, creating the parent
// directory if needed. Uses O_CREATE|O_EXCL so we crash loudly if a file
// already sits at the canonical path — that would mean archivePlanFile
// failed silently, which the caller wants to know about.
func writePlanFile(canonicalPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		return fmt.Errorf("mkdir plan dir: %w", err)
	}
	f, err := os.OpenFile(canonicalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create plan file: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(canonicalPath)
		return fmt.Errorf("write plan file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close plan file: %w", err)
	}
	return nil
}
