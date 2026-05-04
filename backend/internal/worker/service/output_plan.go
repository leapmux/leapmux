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

// snapshotTimestampLayout is the compact RFC3339 format used in plan-snapshot
// filenames. Lexicographic sort matches chronological order; no ':' so the
// names work on Windows. Within-second collisions are handled by the counter
// suffix in snapshotPlanFile.
const snapshotTimestampLayout = "20060102T150405Z"

// planSnapshotCollisionRetries bounds the suffix counter used when two
// snapshots target the same second.
const planSnapshotCollisionRetries = 1000

// planSuffixCollisionRetries bounds the integer suffix probed when two agents
// in the same month try to claim the same canonical filename. The first agent
// wins `<title>.md`; subsequent agents fall through to `<title>.2.md`,
// `<title>.3.md`, ...
const planSuffixCollisionRetries = 1000

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
	return filepath.Join(rootDir, plansDirName, fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%02d", int(now.Month()))), nil
}

// snapshotPlanFile renames `currentPath` to `<base>.<timestamp>.md` (preserving
// the original sanitized-title stem) so the new content can take the
// canonical path. Returns the snapshot path on success. Errors are logged by
// the caller; we never want a failed snapshot to block writing the new plan,
// since the new content is the user's intent.
func (h *OutputHandler) snapshotPlanFile(currentPath string, now time.Time) (string, error) {
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
		// back to using the full name as the stem so the snapshot still
		// gets a unique suffix.
		stem = file
	}

	stamp := now.UTC().Format(snapshotTimestampLayout)
	for counter := 0; counter <= planSnapshotCollisionRetries; counter++ {
		var snapshotPath string
		if counter == 0 {
			snapshotPath = filepath.Join(dir, stem+"."+stamp+".md")
		} else {
			snapshotPath = filepath.Join(dir, fmt.Sprintf("%s.%s.%d.md", stem, stamp, counter))
		}
		if _, err := os.Stat(snapshotPath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat snapshot target: %w", err)
		}
		if err := os.Rename(currentPath, snapshotPath); err != nil {
			return "", fmt.Errorf("snapshot plan file: %w", err)
		}
		return snapshotPath, nil
	}
	return "", fmt.Errorf("too many snapshot collisions for %q", currentPath)
}

// writePlanFile writes content to a file under `dir` whose stem is derived
// from `title`. The first attempt uses `<title>.md`; if O_EXCL fails because
// another agent already owns that name, subsequent attempts append `.2`,
// `.3`, ... up to planSuffixCollisionRetries. Returns the path of the file
// actually written.
//
// O_EXCL closes the TOCTOU window: even if two agents probe the same
// candidate concurrently, exactly one open() succeeds and the loser falls
// through to the next suffix.
func writePlanFile(dir, title string, content []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir plan dir: %w", err)
	}
	stem := agent.SanitizePlanFilenameTitle(title)
	for counter := 1; counter <= planSuffixCollisionRetries; counter++ {
		var candidate string
		if counter == 1 {
			candidate = filepath.Join(dir, stem+".md")
		} else {
			candidate = filepath.Join(dir, fmt.Sprintf("%s.%d.md", stem, counter))
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("create plan file: %w", err)
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			_ = os.Remove(candidate)
			return "", fmt.Errorf("write plan file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close plan file: %w", err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("too many plan filename collisions in %q for title %q", dir, title)
}
