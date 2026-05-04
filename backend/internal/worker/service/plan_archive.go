package service

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/periodic"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	planArchiveInterval = 24 * time.Hour
	// planArchiveJitter matches cleanupJitter for consistency.
	planArchiveJitter = 5 * time.Minute
	planArchiveTmpExt = ".zip.tmp"
	planArchiveZipExt = ".zip"
	// planArchiveSkipLogIDCap bounds the number of agent IDs included in a
	// single "skipped (active agent)" log line so very-large daily logs stay
	// readable.
	planArchiveSkipLogIDCap = 5
)

// plansDirName is the subdirectory of the data dir that holds plan files.
// Shared between the plan-write path (resolvePlanDir) and the plan-archive
// task so the layout has a single source of truth.
const plansDirName = "plans"

// planArchiveStats summarizes the work performed by zipPlanYearDir.
type planArchiveStats struct {
	// Files counts regular files written into the zip (not directories or
	// skipped non-regular entries).
	Files int
	// Bytes is the sum of source file sizes (uncompressed). Useful for
	// capacity planning; the on-disk zip size is incidental and varies with
	// the compression ratio.
	Bytes int64
}

// StartPlanArchiveLoop spawns a background goroutine that compresses old plan
// year directories into per-year zip files (`<dataDir>/plans/<YYYY>.zip`).
// Runs once shortly after worker startup and then once per
// planArchiveInterval, honoring ctx.Done() for graceful shutdown.
//
// Distinct from snapshotPlanFile in output_plan.go: that operation
// timestamps an individual plan revision in place; this one rolls an entire
// year of revisions into a single zip and removes the source dir.
func StartPlanArchiveLoop(ctx context.Context, dataDir string, queries *db.Queries) {
	periodic.Start(ctx, periodic.Schedule{Interval: planArchiveInterval, Jitter: planArchiveJitter}, func(ctx context.Context) {
		runPlanArchive(ctx, dataDir, queries, time.Now().UTC())
	})
}

// runPlanArchive performs one cleanup pass. Strict cancellation contract:
// if ctx is already canceled when called, returns immediately without
// touching the filesystem.
func runPlanArchive(ctx context.Context, dataDir string, queries *db.Queries, now time.Time) {
	if err := ctx.Err(); err != nil {
		return
	}

	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		slog.Error("plan archive: resolve data dir", "error", err)
		return
	}
	plansDir := filepath.Join(absDataDir, plansDirName)

	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		slog.Error("plan archive: read plans dir", "dir", plansDir, "error", err)
		return
	}

	cleanupOrphanTmpFiles(plansDir, entries)

	cutoffYear := now.Year() - 2
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return
		}
		if !e.IsDir() {
			continue
		}
		year, ok := parseYearDirName(e.Name())
		if !ok {
			continue
		}
		if year > cutoffYear {
			continue
		}
		if err := archivePlanYear(ctx, plansDir, year, queries); err != nil {
			slog.Error("plan archive: year failed", "year", year, "error", err)
		}
	}
}

// cleanupOrphanTmpFiles removes any *.zip.tmp files left from a prior
// crashed pass. Errors are logged and ignored — the next pass will retry.
func cleanupOrphanTmpFiles(plansDir string, entries []fs.DirEntry) {
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), planArchiveTmpExt) {
			continue
		}
		path := filepath.Join(plansDir, e.Name())
		if err := os.Remove(path); err != nil {
			slog.Warn("plan archive: failed to remove orphan tmp", "path", path, "error", err)
		}
	}
}

// parseYearDirName returns (year, true) iff name is exactly four ASCII
// digits forming a plausible year. Anything else (e.g. "2024backup",
// "abc", "999") is rejected so we don't accidentally archive non-plan
// directories.
func parseYearDirName(name string) (int, bool) {
	if len(name) != 4 {
		return 0, false
	}
	year := 0
	for _, r := range name {
		if r < '0' || r > '9' {
			return 0, false
		}
		year = year*10 + int(r-'0')
	}
	return year, true
}

// archivePlanYear handles one year directory. The order of safety checks is:
// (1) `<year>.zip` already exists → recovery state, warn and skip; (2) any
// active agent still references the year dir → info-log and skip; (3)
// otherwise zip + rename + remove.
func archivePlanYear(ctx context.Context, plansDir string, year int, queries *db.Queries) error {
	yearStr := strconv.Itoa(year)
	yearDir := filepath.Join(plansDir, yearStr)
	finalZip := filepath.Join(plansDir, yearStr+planArchiveZipExt)
	tmpZip := filepath.Join(plansDir, yearStr+planArchiveTmpExt)

	if _, err := os.Stat(finalZip); err == nil {
		slog.Warn("plan archive: year skipped (zip + dir both present)", "year", year, "zip", finalZip, "dir", yearDir)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat final zip: %w", err)
	}

	dirPrefix := yearDir + string(filepath.Separator)
	ids, err := queries.ListAgentIDsWithPlanInDir(ctx, dirPrefix)
	if err != nil {
		return fmt.Errorf("query active agents: %w", err)
	}
	if len(ids) > 0 {
		logIDs := ids
		if len(logIDs) > planArchiveSkipLogIDCap {
			logIDs = logIDs[:planArchiveSkipLogIDCap]
		}
		slog.Info("plan archive: year skipped (active agent)", "year", year, "agent_count", len(ids), "agent_ids", logIDs)
		return nil
	}

	start := time.Now()
	stats, err := zipPlanYearDir(ctx, yearDir, tmpZip, yearStr)
	if err != nil {
		_ = os.Remove(tmpZip)
		return fmt.Errorf("zip year dir: %w", err)
	}

	if err := os.Rename(tmpZip, finalZip); err != nil {
		_ = os.Remove(tmpZip)
		return fmt.Errorf("rename tmp zip: %w", err)
	}

	if err := os.RemoveAll(yearDir); err != nil {
		// The zip is durable; failing to remove the source dir is recoverable
		// (the next pass will see zip+dir and warn). Log and return success
		// so we don't double-archive on the next run.
		slog.Warn("plan archive: failed to remove source dir after archive", "year", year, "dir", yearDir, "error", err)
	}

	slog.Info("plan archive: year archived", "year", year, "files", stats.Files, "bytes", stats.Bytes, "duration", time.Since(start))
	return nil
}

// zipPlanYearDir streams srcDir into a new zip at zipPath with topLevel as
// the first path segment. Honors ctx.Done() between files. Skips symlinks
// and non-regular files with a warn log; the resulting archive contains
// only directories and regular files.
//
// Finalization order (matters for ZIP validity + durability):
//  1. Create the file
//  2. zip.NewWriter
//  3. Walk + write entries
//  4. zw.Close() — flushes the central directory into the file
//  5. f.Sync() — durably persists the bytes
//  6. f.Close() — releases the FD
//
// The caller renames the file into place after this function returns.
func zipPlanYearDir(ctx context.Context, srcDir, zipPath, topLevel string) (planArchiveStats, error) {
	var stats planArchiveStats

	f, err := os.Create(zipPath)
	if err != nil {
		return stats, fmt.Errorf("create zip file: %w", err)
	}
	zw := zip.NewWriter(f)

	// Write the explicit top-level directory entry so the archive always
	// starts with `<topLevel>/` even when srcDir is empty.
	rootHeader := &zip.FileHeader{Name: topLevel + "/", Method: zip.Store}
	rootHeader.SetMode(0o755 | fs.ModeDir)
	if _, err := zw.CreateHeader(rootHeader); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return stats, fmt.Errorf("write root header: %w", err)
	}

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == srcDir {
			// Skip the root itself; we already wrote a synthetic entry above.
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("rel: %w", err)
		}
		entryName := filepath.ToSlash(filepath.Join(topLevel, rel))

		fi, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %q: %w", path, err)
		}
		mode := fi.Mode()
		if !mode.IsRegular() && !mode.IsDir() {
			// Symlinks (which Lstat reports without ModeDir even when they
			// point to a directory) and other non-regular entries (devices,
			// sockets, fifos) are not represented in the archive.
			slog.Warn("plan archive: skipping non-regular entry", "path", path, "mode", mode.String())
			return nil
		}

		header, err := zip.FileInfoHeader(fi)
		if err != nil {
			return fmt.Errorf("header for %q: %w", path, err)
		}
		header.Name = entryName
		if mode.IsDir() {
			header.Name += "/"
			header.Method = zip.Store
			if _, err := zw.CreateHeader(header); err != nil {
				return fmt.Errorf("write dir header %q: %w", entryName, err)
			}
			return nil
		}

		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("write file header %q: %w", entryName, err)
		}
		src, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %q: %w", path, err)
		}
		n, err := io.Copy(w, src)
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("copy %q: %w", path, err)
		}
		stats.Files++
		stats.Bytes += n
		return nil
	})
	if walkErr != nil {
		_ = zw.Close()
		_ = f.Close()
		return stats, walkErr
	}

	if err := zw.Close(); err != nil {
		_ = f.Close()
		return stats, fmt.Errorf("close zip writer: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return stats, fmt.Errorf("sync zip file: %w", err)
	}
	if err := f.Close(); err != nil {
		return stats, fmt.Errorf("close zip file: %w", err)
	}
	return stats, nil
}
