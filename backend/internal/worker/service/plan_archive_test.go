package service

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// fixedNow is the wall-clock used in plan-archive tests so the year-window
// math is deterministic. With currentYear=2026, the cutoff is 2024 — i.e.
// 2024 and earlier should be archived, 2025/2026 kept.
var fixedNow = time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

// writeTestPlanFile creates a file at the given path with the given content,
// creating any missing parent directories.
func writeTestPlanFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// readZipEntries returns the filenames + contents of a zip in iteration order.
// Directory entries appear with their trailing `/`.
func readZipEntries(t *testing.T, zipPath string) []zipEntry {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = zr.Close() })

	var out []zipEntry
	for _, f := range zr.File {
		entry := zipEntry{Name: f.Name}
		if !strings.HasSuffix(f.Name, "/") {
			rc, err := f.Open()
			require.NoError(t, err)
			data, err := io.ReadAll(rc)
			require.NoError(t, err)
			_ = rc.Close()
			entry.Body = string(data)
		}
		out = append(out, entry)
	}
	return out
}

type zipEntry struct {
	Name string
	Body string
}

func TestPlanArchive_HappyPath(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2023", "01", "a.md"), "year 2023 file a")
	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "02", "b.md"), "year 2024 file b")
	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "03", "c.md"), "year 2024 file c")
	writeTestPlanFile(t, filepath.Join(plansDir, "2025", "04", "d.md"), "year 2025 file d")
	writeTestPlanFile(t, filepath.Join(plansDir, "2026", "05", "e.md"), "year 2026 file e")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	// 2023 and 2024 archived: zip exists, source dir gone.
	assert.FileExists(t, filepath.Join(plansDir, "2023.zip"))
	assert.FileExists(t, filepath.Join(plansDir, "2024.zip"))
	assert.NoDirExists(t, filepath.Join(plansDir, "2023"))
	assert.NoDirExists(t, filepath.Join(plansDir, "2024"))

	// 2025 and 2026 untouched.
	assert.NoFileExists(t, filepath.Join(plansDir, "2025.zip"))
	assert.NoFileExists(t, filepath.Join(plansDir, "2026.zip"))
	assert.DirExists(t, filepath.Join(plansDir, "2025"))
	assert.DirExists(t, filepath.Join(plansDir, "2026"))
	assert.FileExists(t, filepath.Join(plansDir, "2025", "04", "d.md"))
	assert.FileExists(t, filepath.Join(plansDir, "2026", "05", "e.md"))
}

func TestPlanArchive_ZipStructureAndExactRootEntry(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "02", "b.md"), "body-b")
	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "03", "c.md"), "body-c")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	entries := readZipEntries(t, filepath.Join(plansDir, "2024.zip"))
	require.NotEmpty(t, entries)
	// The first entry must be exactly `2024/` — not `2024/.`, not unprefixed,
	// not missing the trailing slash.
	assert.Equal(t, "2024/", entries[0].Name, "first entry should be the explicit top-level directory")

	got := map[string]string{}
	for _, e := range entries {
		got[e.Name] = e.Body
	}
	// Nested directories carry the trailing slash, files have slash-form paths
	// and exact byte-for-byte content.
	assert.Equal(t, "", got["2024/02/"])
	assert.Equal(t, "body-b", got["2024/02/b.md"])
	assert.Equal(t, "", got["2024/03/"])
	assert.Equal(t, "body-c", got["2024/03/c.md"])
}

func TestPlanArchive_NoOpWhenPlansDirMissing(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()

	// Sanity: plans dir does not exist yet.
	_, err := os.Stat(filepath.Join(dataDir, "plans"))
	require.ErrorIs(t, err, os.ErrNotExist)

	// Should not error, should not create the dir.
	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	_, err = os.Stat(filepath.Join(dataDir, "plans"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestPlanArchive_NoOpWhenNoOldYears(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2025", "04", "d.md"), "y25")
	writeTestPlanFile(t, filepath.Join(plansDir, "2026", "05", "e.md"), "y26")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	assert.DirExists(t, filepath.Join(plansDir, "2025"))
	assert.DirExists(t, filepath.Join(plansDir, "2026"))
	assert.NoFileExists(t, filepath.Join(plansDir, "2025.zip"))
	assert.NoFileExists(t, filepath.Join(plansDir, "2026.zip"))
}

func TestPlanArchive_NonYearSubdirsIgnored(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	require.NoError(t, os.MkdirAll(filepath.Join(plansDir, "scratch"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(plansDir, "2024backup"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(plansDir, "abc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(plansDir, "999"), 0o755))   // 3 digits, not a year
	require.NoError(t, os.MkdirAll(filepath.Join(plansDir, "20240"), 0o755)) // 5 digits, not a year

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	for _, name := range []string{"scratch", "2024backup", "abc", "999", "20240"} {
		assert.DirExists(t, filepath.Join(plansDir, name), "%s must not be touched", name)
		assert.NoFileExists(t, filepath.Join(plansDir, name+".zip"), "%s.zip must not be created", name)
	}
}

// makeAgentWithPlanPath inserts an agent and points its plan_file_path at the
// given path. Used to drive the DB safeguard.
func makeAgentWithPlanPath(t *testing.T, queries *db.Queries, agentID, planPath string) {
	t.Helper()
	require.NoError(t, queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "test agent",
	}))
	require.NoError(t, queries.UpdateAgentPlan(context.Background(), db.UpdateAgentPlanParams{
		PlanFilePath: planPath,
		PlanTitle:    "irrelevant",
		ID:           agentID,
	}))
}

func TestPlanArchive_DBSafeguardSkipsYearWithActiveAgent(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "foo.md"), "still active")

	absDataDir, err := filepath.Abs(dataDir)
	require.NoError(t, err)
	planPath := filepath.Join(absDataDir, "plans", "2024", "05", "foo.md")
	makeAgentWithPlanPath(t, queries, "agent-1", planPath)

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	// Year not archived: source dir intact, no zip.
	assert.DirExists(t, filepath.Join(plansDir, "2024"))
	assert.FileExists(t, filepath.Join(plansDir, "2024", "05", "foo.md"))
	assert.NoFileExists(t, filepath.Join(plansDir, "2024.zip"))
}

// TestPlanArchive_DBSafeguardWithGlobMetacharsInDataDir verifies that the
// safeguard uses literal-byte prefix matching (instr) and not GLOB/LIKE — a
// data dir whose name contains `*` or `[` must not produce false negatives.
func TestPlanArchive_DBSafeguardWithGlobMetacharsInDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reserves * and [ in filenames")
	}
	_, queries := setupTestDB(t)
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "data*[abc]?")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "foo.md"), "still active")

	absDataDir, err := filepath.Abs(dataDir)
	require.NoError(t, err)
	planPath := filepath.Join(absDataDir, "plans", "2024", "05", "foo.md")
	makeAgentWithPlanPath(t, queries, "agent-1", planPath)

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	// If we used GLOB, `*` in the prefix would expand and silently match
	// nothing (false negative) — and the year would be archived, deleting
	// the active agent's plan dir. With instr(), the prefix is matched as
	// literal bytes and the year is correctly skipped.
	assert.DirExists(t, filepath.Join(plansDir, "2024"))
	assert.FileExists(t, filepath.Join(plansDir, "2024", "05", "foo.md"))
	assert.NoFileExists(t, filepath.Join(plansDir, "2024.zip"))
}

func TestPlanArchive_OrphanTmpRemoved(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "x.md"), "real content")

	orphan := filepath.Join(plansDir, "2024.zip.tmp")
	writeTestPlanFile(t, orphan, "garbage from prior crash")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	// Orphan removed; year archived normally.
	assert.NoFileExists(t, orphan)
	assert.FileExists(t, filepath.Join(plansDir, "2024.zip"))
	assert.NoDirExists(t, filepath.Join(plansDir, "2024"))
}

func TestPlanArchive_RecoverySkipWhenZipAndDirBothPresent(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "x.md"), "should not be touched")
	// Pre-existing zip simulates a prior crash between rename and RemoveAll.
	writeTestPlanFile(t, filepath.Join(plansDir, "2024.zip"), "preexisting zip bytes")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	// Both should still be present — no auto-remove, no re-zip.
	assert.FileExists(t, filepath.Join(plansDir, "2024.zip"))
	assert.DirExists(t, filepath.Join(plansDir, "2024"))
	assert.FileExists(t, filepath.Join(plansDir, "2024", "05", "x.md"))

	// Confirm the existing zip was not overwritten.
	got, err := os.ReadFile(filepath.Join(plansDir, "2024.zip"))
	require.NoError(t, err)
	assert.Equal(t, "preexisting zip bytes", string(got))
}

func TestPlanArchive_SymlinksSkipped(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "real.md"), "real")

	target := filepath.Join(plansDir, "2024", "05", "real.md")
	link := filepath.Join(plansDir, "2024", "05", "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	assert.FileExists(t, filepath.Join(plansDir, "2024.zip"))
	assert.NoDirExists(t, filepath.Join(plansDir, "2024"))

	entries := readZipEntries(t, filepath.Join(plansDir, "2024.zip"))
	for _, e := range entries {
		assert.NotEqual(t, "2024/05/link.md", e.Name, "symlink must not appear in zip")
	}
}

func TestPlanArchive_IdempotentRerun(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "a.md"), "a")
	writeTestPlanFile(t, filepath.Join(plansDir, "2025", "05", "b.md"), "b")

	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	zipInfo1, err := os.Stat(filepath.Join(plansDir, "2024.zip"))
	require.NoError(t, err)

	// Second pass should be a no-op: 2024.zip already exists, 2025 still in
	// the keep window.
	runPlanArchive(context.Background(), dataDir, queries, fixedNow)

	zipInfo2, err := os.Stat(filepath.Join(plansDir, "2024.zip"))
	require.NoError(t, err)
	assert.Equal(t, zipInfo1.Size(), zipInfo2.Size(), "zip should be unchanged")
	assert.Equal(t, zipInfo1.ModTime(), zipInfo2.ModTime(), "zip should not be rewritten")
	assert.DirExists(t, filepath.Join(plansDir, "2025"))
}

func TestPlanArchive_PreCanceledCtxIsCompleteNoOp(t *testing.T) {
	_, queries := setupTestDB(t)
	dataDir := t.TempDir()
	plansDir := filepath.Join(dataDir, "plans")

	writeTestPlanFile(t, filepath.Join(plansDir, "2024", "05", "x.md"), "x")
	orphan := filepath.Join(plansDir, "2024.zip.tmp")
	writeTestPlanFile(t, orphan, "orphan tmp from prior crash")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runPlanArchive(ctx, dataDir, queries, fixedNow)

	// Strict cancellation: nothing on disk changed.
	assert.FileExists(t, orphan, "orphan tmp must not be cleaned up under canceled ctx")
	assert.NoFileExists(t, filepath.Join(plansDir, "2024.zip"))
	assert.DirExists(t, filepath.Join(plansDir, "2024"))
	assert.FileExists(t, filepath.Join(plansDir, "2024", "05", "x.md"))
}
