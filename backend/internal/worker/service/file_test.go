package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/pathutil"
)

func TestListDirectory_Truncation(t *testing.T) {
	t.Parallel()

	t.Run("below limit is not truncated", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.False(t, truncated, "expected truncated=false for 10 entries")
		assert.Len(t, entries, 10)
	})

	t.Run("exactly at limit is not truncated", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < maxDirEntries; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.False(t, truncated, "expected truncated=false for exactly %d entries", maxDirEntries)
		assert.Len(t, entries, maxDirEntries)
	})

	t.Run("above limit is truncated", func(t *testing.T) {
		dir := t.TempDir()
		total := maxDirEntries + 50
		for i := 0; i < total; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.True(t, truncated, "expected truncated=true for %d entries", total)
		assert.Len(t, entries, maxDirEntries)
	})
}

func TestListDirectory_SortOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create files and directories with names that test sort order.
	files := []string{"banana.txt", "apple.txt", "Cherry.txt"}
	dirs := []string{"zoo", "alpha"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range dirs {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)
	assert.False(t, truncated, "unexpected truncation")

	// Expected order: directories first (case-insensitive), then files (case-insensitive).
	expected := []struct {
		name  string
		isDir bool
	}{
		{"alpha", true},
		{"zoo", true},
		{"apple.txt", false},
		{"banana.txt", false},
		{"Cherry.txt", false},
	}

	require.Len(t, entries, len(expected))
	for i, want := range expected {
		assert.Equal(t, want.name, entries[i].Name, "entry[%d].Name", i)
		assert.Equal(t, want.isDir, entries[i].IsDir, "entry[%d].IsDir", i)
	}
}

// TestListDirectory_TotalEntries pins the count the sidebar's truncation notice
// reports, so it can say how much it is NOT showing rather than only that
// something is missing.
//
// Counted after the dirs-only filter and before the cut, so it describes the
// same population the returned entries were drawn from.
func TestListDirectory_TotalEntries(t *testing.T) {
	t.Parallel()

	t.Run("equals the entry count when nothing was cut", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644))
		}

		entries, truncated, total, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.False(t, truncated)
		assert.Equal(t, len(entries), total)
	})

	t.Run("reports the pre-truncation count", func(t *testing.T) {
		dir := t.TempDir()
		const written = maxDirEntries + 42
		for i := 0; i < written; i++ {
			require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%04d.txt", i)), nil, 0o644))
		}

		entries, truncated, total, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.True(t, truncated)
		assert.Len(t, entries, maxDirEntries)
		assert.Equal(t, written, total, "the notice needs what the directory really held")
	})

	// dirs_only filters BEFORE the count, so the total must describe
	// directories alone -- not every entry the directory happens to hold.
	t.Run("counts only directories under dirs_only", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 3; i++ {
			require.NoError(t, os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%d", i)), 0o755))
		}
		for i := 0; i < 20; i++ {
			require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%02d.txt", i)), nil, 0o644))
		}

		entries, truncated, total, err := listDirectory(dir, dir, 0, 0, true)
		require.NoError(t, err)
		assert.False(t, truncated)
		assert.Len(t, entries, 3)
		assert.Equal(t, 3, total)
	})
}

func TestListDirectory_TruncationKeepsDirsFirst(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create enough directories and files to exceed the limit.
	// 200 directories + 200 files = 400 > 256.
	// After truncation, all 200 dirs should be kept, plus 56 files.
	numDirs := 200
	numFiles := 200
	for i := 0; i < numDirs; i++ {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < numFiles; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)
	assert.True(t, truncated, "expected truncated=true")
	require.Len(t, entries, maxDirEntries)

	// All 100 directories should appear before any files.
	dirCount := 0
	for i, e := range entries {
		if e.IsDir {
			dirCount++
		} else if dirCount < numDirs {
			assert.Fail(t, "file appeared before all directories", "file %q at index %d", e.Name, i)
			break
		}
	}
	assert.Equal(t, numDirs, dirCount)

	// The remaining entries should be files in alphabetical order.
	fileEntries := entries[numDirs:]
	for i := 1; i < len(fileEntries); i++ {
		assert.GreaterOrEqual(t, fileEntries[i].Name, fileEntries[i-1].Name, "files not sorted at index %d", numDirs+i)
	}
}

func TestFileInfoToProto_Hidden(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a hidden file and a regular file.
	hiddenPath := filepath.Join(dir, ".hidden")
	regularPath := filepath.Join(dir, "visible.txt")
	if err := os.WriteFile(hiddenPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regularPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	hiddenInfo, err := os.Stat(hiddenPath)
	require.NoError(t, err)
	regularInfo, err := os.Stat(regularPath)
	require.NoError(t, err)

	hiddenProto := fileInfoToProto(hiddenInfo, hiddenPath)
	assert.True(t, hiddenProto.Hidden, "expected Hidden=true for %q", hiddenPath)

	regularProto := fileInfoToProto(regularInfo, regularPath)
	assert.False(t, regularProto.Hidden, "expected Hidden=false for %q", regularPath)
}

func TestListDirectory_HiddenField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a mix of hidden and regular entries.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	entries, _, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)

	for _, e := range entries {
		expectHidden := e.Name[0] == '.'
		assert.Equal(t, expectHidden, e.Hidden, "entry %q: Hidden", e.Name)
	}
}

func TestListDirectory_MergeHiddenDirs(t *testing.T) {
	t.Parallel()

	t.Run("hidden top-level dir is merged with hidden flag", func(t *testing.T) {
		dir := t.TempDir()
		// .github/workflows — hidden dir should be merged, with hidden flag propagated.
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		// Merged Name is a display-only label that always uses "/".
		assert.Equal(t, ".github/workflows", entries[0].Name)
		assert.True(t, entries[0].Hidden, "expected Hidden=true for merged .github/workflows")
	})

	t.Run("hidden child propagates hidden flag through merge", func(t *testing.T) {
		dir := t.TempDir()
		// src/.internal/utils — merge should go through .internal, propagating hidden.
		if err := os.MkdirAll(filepath.Join(dir, "src", ".internal", "utils"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "src/.internal/utils", entries[0].Name)
		assert.True(t, entries[0].Hidden, "expected Hidden=true when a hidden dir is in the merged path")
	})

	t.Run("non-hidden single-child dirs merge without hidden flag", func(t *testing.T) {
		dir := t.TempDir()
		// src/main/java — all visible, should merge normally, not hidden.
		if err := os.MkdirAll(filepath.Join(dir, "src", "main", "java"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "src/main/java", entries[0].Name)
		assert.False(t, entries[0].Hidden, "expected Hidden=false for non-hidden merged path")
	})
}

func TestListDirectory_DirsOnly(t *testing.T) {
	t.Parallel()

	t.Run("filters out files", func(t *testing.T) {
		dir := t.TempDir()
		numDirs := 5
		numFiles := 10
		for i := 0; i < numDirs; i++ {
			if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < numFiles; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, _, err := listDirectory(dir, dir, 0, 0, true)
		require.NoError(t, err)
		assert.False(t, truncated, "expected truncated=false")
		assert.Len(t, entries, numDirs)
		for _, e := range entries {
			assert.True(t, e.IsDir, "expected only directories, got file %q", e.Name)
		}
	})

	t.Run("truncation counts only dirs", func(t *testing.T) {
		dir := t.TempDir()
		// Create more dirs than the limit, plus many files.
		numDirs := maxDirEntries + 10
		numFiles := 50
		for i := 0; i < numDirs; i++ {
			if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < numFiles; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, _, err := listDirectory(dir, dir, 0, 0, true)
		require.NoError(t, err)
		assert.True(t, truncated, "expected truncated=true")
		assert.Len(t, entries, maxDirEntries)
		for _, e := range entries {
			assert.True(t, e.IsDir, "expected only directories, got file %q", e.Name)
		}
	})

	t.Run("includes symlinked directories", func(t *testing.T) {
		dir := t.TempDir()
		// Create a real directory and a symlink pointing to it.
		realDir := filepath.Join(dir, "realdir")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, filepath.Join(dir, "linkdir")); err != nil {
			t.Fatal(err)
		}
		// Also create a regular file and a symlink to a file (both should be excluded).
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		fileTarget := filepath.Join(dir, "file.txt")
		if err := os.Symlink(fileTarget, filepath.Join(dir, "linkfile")); err != nil {
			t.Fatal(err)
		}

		entries, _, _, err := listDirectory(dir, dir, 0, 0, true)
		require.NoError(t, err)
		require.Len(t, entries, 2, "expected 2 entries (realdir + linkdir)")
		for _, e := range entries {
			assert.True(t, e.IsDir, "expected only directories, got non-dir %q", e.Name)
		}
	})

	t.Run("symlinked directories sort with real directories", func(t *testing.T) {
		dir := t.TempDir()
		// Create: aaa_file (file), bbb_dir (dir), ccc_link (symlink->dir), ddd_file (file).
		if err := os.WriteFile(filepath.Join(dir, "aaa_file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "bbb_dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "bbb_dir")
		if err := os.Symlink(target, filepath.Join(dir, "ccc_link")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ddd_file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		entries, _, _, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		// Directories (real + symlinked) should come first.
		require.GreaterOrEqual(t, len(entries), 2, "expected at least 2 entries")
		// First two entries should be directories (bbb_dir and ccc_link, both dirs).
		for _, e := range entries[:2] {
			assert.True(t, e.IsDir, "expected directory in first two entries, got file %q", e.Name)
		}
		// Last two entries should be files.
		for _, e := range entries[2:] {
			assert.False(t, e.IsDir, "expected file in last two entries, got dir %q", e.Name)
		}
	})
}

// TestReadFile_MetaOnlyIfTruncated verifies that when the flag is set and the
// file's total size exceeds the read window, the handler returns total_size
// with an empty content payload — letting clients detect oversize files in a
// single round trip without the matching byte payload.
// TestReadFile_CarriesModTime pins that a read answers with the file's
// modification time, on BOTH response paths.
//
// The file viewer shows the same three-dot menu the sidebar tree does, and
// without this field it would need a second StatFile round trip just to fill in
// the Modified row. The handler already holds the FileInfo, so the field costs
// no extra syscall -- which is the whole reason it lives on this response.
func TestReadFile_CarriesModTime(t *testing.T) {
	t.Parallel()

	// A fixed past instant, so the assertion pins the file's mtime rather than
	// "roughly now", which any clock would satisfy. The nanosecond tail ends
	// in 00 because NTFS stores file times in 100ns ticks: a value off that
	// grid comes back rounded on Windows, and the round trip would fail there.
	// Seven significant digits still prove sub-microsecond fidelity.
	want := time.Date(2026, 3, 4, 5, 6, 7, 123456700, time.UTC)

	readModTime := func(t *testing.T, metaOnly bool, limit int64) string {
		t.Helper()
		svc, d, w := setupTestService(t)
		path := filepath.Join(svc.HomeDir, "stamped.txt")
		require.NoError(t, os.WriteFile(path, repeatedByte(4096, 'a'), 0o644))
		require.NoError(t, os.Chtimes(path, want, want))

		dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
			Path:                path,
			Limit:               limit,
			MetaOnlyIfTruncated: metaOnly,
		}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.ReadFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return resp.GetModTime()
	}

	t.Run("normal read", func(t *testing.T) {
		assert.Equal(t, formatModTime(want), readModTime(t, false, 8192))
	})

	// The meta-only short-circuit returns before the read, so it needs its own
	// case -- it is the path the viewer takes for an oversize image.
	t.Run("meta-only short circuit", func(t *testing.T) {
		assert.Equal(t, formatModTime(want), readModTime(t, true, 1024))
	})

	t.Run("matches what StatFile reports", func(t *testing.T) {
		svc, d, w := setupTestService(t)
		path := filepath.Join(svc.HomeDir, "agree.txt")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(path, want, want))

		dispatch(d, "StatFile", &leapmuxv1.StatFileRequest{Path: path}, w)
		require.Len(t, w.responses, 1)
		var statResp leapmuxv1.StatFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &statResp))

		assert.Equal(t, statResp.GetInfo().GetModTime(), readModTime(t, false, 8192),
			"a reader must not have to choose which RPC to trust")
	})
}

func TestReadFile_MetaOnlyIfTruncated(t *testing.T) {
	t.Parallel()

	t.Run("oversize: empty content with total_size", func(t *testing.T) {
		svc, d, w := setupTestService(t)

		path := filepath.Join(svc.HomeDir, "big.bin")
		const totalSize = 4096
		require.NoError(t, os.WriteFile(path, repeatedByte(totalSize, 'a'), 0o644))

		dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
			Path:                path,
			Limit:               1024,
			MetaOnlyIfTruncated: true,
		}, w)

		require.Empty(t, w.errors, "expected no error")
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.ReadFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.EqualValues(t, totalSize, resp.GetTotalSize())
		assert.Empty(t, resp.GetContent(), "expected empty content when truncated and meta-only set")
	})

	t.Run("within limit: full content with total_size", func(t *testing.T) {
		svc, d, w := setupTestService(t)

		path := filepath.Join(svc.HomeDir, "small.bin")
		payload := repeatedByte(100, 'x')
		require.NoError(t, os.WriteFile(path, payload, 0o644))

		dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
			Path:                path,
			Limit:               1024,
			MetaOnlyIfTruncated: true,
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.ReadFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.EqualValues(t, len(payload), resp.GetTotalSize())
		assert.Equal(t, payload, resp.GetContent())
	})

	t.Run("flag off: oversize returns truncated content (legacy behavior)", func(t *testing.T) {
		svc, d, w := setupTestService(t)

		path := filepath.Join(svc.HomeDir, "big-legacy.bin")
		const totalSize = 4096
		const limit = 1024
		require.NoError(t, os.WriteFile(path, repeatedByte(totalSize, 'b'), 0o644))

		dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
			Path:  path,
			Limit: limit,
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.ReadFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.EqualValues(t, totalSize, resp.GetTotalSize())
		assert.Len(t, resp.GetContent(), limit, "legacy mode must keep returning truncated bytes")
	})

	t.Run("offset counted toward the truncation threshold", func(t *testing.T) {
		svc, d, w := setupTestService(t)

		path := filepath.Join(svc.HomeDir, "with-offset.bin")
		const totalSize = 4096
		require.NoError(t, os.WriteFile(path, repeatedByte(totalSize, 'c'), 0o644))

		// offset + limit = 4096 = totalSize, so the read window covers the
		// whole file and the meta-only short-circuit must NOT fire.
		dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
			Path:                path,
			Offset:              3072,
			Limit:               1024,
			MetaOnlyIfTruncated: true,
		}, w)

		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)

		var resp leapmuxv1.ReadFileResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.EqualValues(t, totalSize, resp.GetTotalSize())
		assert.Len(t, resp.GetContent(), 1024)
	})
}

// repeatedByte returns a slice of length n filled with the given byte. Used
// by the ReadFile tests to construct payloads of a specific size without
// staging the expected bytes inline in each case.
func repeatedByte(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// TestReadFile_ClampsAnOversizeLimit pins the upper bound on a
// request-supplied read window.
//
// limit comes straight off the wire and is used as make([]byte, limit),
// so unclamped it lets one request choose the worker's allocation size.
// A value above the producer ceiling is also unserviceable on its own
// terms: the response it builds is one the channel refuses, and on the
// unary path that refusal reaches the caller as nothing at all.
//
// The file is sparse (Truncate, not written bytes) so the boundary is
// exercised without materialising it, and meta_only_if_truncated is the
// cheap observation: the clamp is what makes a file this size count as
// truncated, so an unclamped limit returns content here instead.
func TestReadFile_ClampsAnOversizeLimit(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)

	path := filepath.Join(svc.HomeDir, "sparse.bin")
	f, err := os.Create(path)
	require.NoError(t, err)
	// Larger than the clamp, so the clamp decides the outcome.
	maxRead := svc.payloadBudget(nil)
	totalSize := maxRead + (1 << 20)
	require.NoError(t, f.Truncate(totalSize))
	require.NoError(t, f.Close())

	dispatch(d, "ReadFile", &leapmuxv1.ReadFileRequest{
		Path: path,
		// Far above the ceiling, and above the file: without the clamp
		// offset+limit exceeds total_size, so nothing looks truncated.
		Limit:               maxRead * 4,
		MetaOnlyIfTruncated: true,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.ReadFileResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.EqualValues(t, totalSize, resp.GetTotalSize())
	assert.Empty(t, resp.GetContent(),
		"a limit above the producer ceiling must be clamped, which makes this file truncated")
}

func TestPayloadBudget_UsesConfiguredMaxMessageSize(t *testing.T) {
	t.Parallel()

	svc := &Service{Config: Config{MaxMessageSize: 2 << 20}}
	assert.Equal(t, int64(2<<20), svc.payloadBudget(nil))

	svc.MaxMessageSize = 0
	assert.Equal(t, int64(contracts.MaxMessageSize), svc.payloadBudget(nil),
		"0 must resolve to the protocol default payload budget")
}

func TestPayloadBudget_UsesNegotiatedChannelBudgetWhenTighter(t *testing.T) {
	t.Parallel()

	svc := &Service{Config: Config{MaxMessageSize: 4 << 20}}
	sender := &budgetWriter{budget: 1 << 20}
	assert.Equal(t, int64(1<<20), svc.payloadBudget(sender),
		"channel negotiated budget must clamp below the worker knob")
	assert.Equal(t, int64(4<<20), svc.payloadBudget(&budgetWriter{budget: 8 << 20}),
		"worker knob must win when the channel budget is higher")
	assert.Equal(t, int64(4<<20), svc.payloadBudget(&budgetWriter{budget: 0}),
		"zero budget (non-channel writer) must fall back to the worker knob")
}

type budgetWriter struct {
	channel.ResponseWriter
	budget int
}

func (w *budgetWriter) MaxPayloadBudget() int { return w.budget }
func (*budgetWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

// The symlink cases below cover the branch isDirEntry exists for. Nothing
// else pins it, and the decorate-then-sort pass in listDirectory is the only
// caller that computes it for every entry.
func TestListDirectory_SymlinkSortsByTarget(t *testing.T) {
	t.Parallel()
	requireSymlinkSupport(t)

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "target-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target-file.txt"), nil, 0o644))
	// Names chosen so a name-only sort would interleave them with the files:
	// "zlink-to-dir" sorts after every file name, so it can only lead the
	// listing if the symlink resolved to a directory.
	require.NoError(t, os.Symlink(filepath.Join(dir, "target-dir"), filepath.Join(dir, "zlink-to-dir")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "target-file.txt"), filepath.Join(dir, "alink-to-file")))

	entries, truncated, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)
	assert.False(t, truncated)

	expected := []struct {
		name  string
		isDir bool
	}{
		{"target-dir", true},
		{"zlink-to-dir", true},
		{"alink-to-file", false},
		{"target-file.txt", false},
	}
	require.Len(t, entries, len(expected))
	for i, want := range expected {
		assert.Equal(t, want.name, entries[i].Name, "entry[%d].Name", i)
		assert.Equal(t, want.isDir, entries[i].IsDir, "entry[%d].IsDir", i)
	}
}

func TestListDirectory_DirsOnlyKeepsSymlinkedDirs(t *testing.T) {
	t.Parallel()
	requireSymlinkSupport(t)

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "real-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real-file.txt"), nil, 0o644))
	require.NoError(t, os.Symlink(filepath.Join(dir, "real-dir"), filepath.Join(dir, "link-to-dir")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "real-file.txt"), filepath.Join(dir, "link-to-file")))

	entries, _, _, err := listDirectory(dir, dir, 0, 0, true)
	require.NoError(t, err)

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
		assert.True(t, e.IsDir, "entry %q must be a directory", e.Name)
	}
	assert.Equal(t, []string{"link-to-dir", "real-dir"}, names)
}

func TestListDirectory_BrokenSymlinkIsSkipped(t *testing.T) {
	t.Parallel()
	requireSymlinkSupport(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kept.txt"), nil, 0o644))
	require.NoError(t, os.Symlink(filepath.Join(dir, "no-such-target"), filepath.Join(dir, "broken")))

	// isDirEntry reports false for a broken symlink (os.Stat fails), so it
	// sorts with the files, and the per-entry os.Stat then drops it.
	entries, _, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)

	require.Len(t, entries, 1)
	assert.Equal(t, "kept.txt", entries[0].Name)
}

// requireSymlinkSupport skips the caller on a platform where os.Symlink
// needs a privilege the test process may not hold (Windows without
// Developer Mode).
func requireSymlinkSupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege")
	}
}

// ---------------------------------------------------------------------------
// ListFilesystemRoots
// ---------------------------------------------------------------------------

// The dispatcher-level test for the file family's newest handler: it drives
// the real registration, the real scope gate and the real owner gate, none of
// which the listDirectory tests above reach.
func TestListFilesystemRoots(t *testing.T) {
	t.Parallel()
	_, d, w := setupTestService(t)

	dispatch(d, "ListFilesystemRoots", &leapmuxv1.ListFilesystemRootsRequest{}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.ListFilesystemRootsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	require.NotEmpty(t, resp.GetRoots())
	assert.Equal(t, pathutil.FilesystemRoots(), resp.GetRoots(),
		"the handler must pass the seam's answer through unreshaped")
	if runtime.GOOS != "windows" {
		assert.Equal(t, []string{"/"}, resp.GetRoots())
	}
}

// The end-to-end contract between the two RPCs: a root this worker reports is
// a path the same worker's ListDirectory accepts.
//
// It asserts "not refused", not "no error". A machine with an empty optical
// drive legitimately answers Internal from os.ReadDir, and that is a valid
// outcome for a listable-but-empty device. What must never happen is
// PermissionDenied (SanitizePath refused the spelling) or InvalidArgument.
func TestListFilesystemRoots_RootsAreListable(t *testing.T) {
	t.Parallel()
	_, d, w := setupTestService(t)

	dispatch(d, "ListFilesystemRoots", &leapmuxv1.ListFilesystemRootsRequest{}, w)
	require.Len(t, w.responses, 1)
	var roots leapmuxv1.ListFilesystemRootsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &roots))

	require.NotEmpty(t, roots.GetRoots())
	for _, root := range roots.GetRoots() {
		lw := &testResponseWriter{channelID: testChannelID}
		dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: root, DirsOnly: true}, lw)

		// One outcome or the other, never neither. Without this the loop below
		// is vacuous for an unregistered handler, an owner gate that refused
		// silently, or a dispatch that routed nowhere -- and the stated
		// contract goes unverified while the test reports success.
		require.Equal(t, 1, len(lw.responses)+len(lw.errors),
			"root %q produced neither a response nor an error", root)

		for _, e := range lw.errors {
			assert.NotEqual(t, codePermissionDenied, e.code, "root %q was refused: %s", root, e.message)
			assert.NotEqual(t, codeInvalidArgument, e.code, "root %q was rejected: %s", root, e.message)
		}
		if len(lw.responses) == 1 {
			var listed leapmuxv1.ListDirectoryResponse
			require.NoError(t, proto.Unmarshal(lw.responses[0].GetPayload(), &listed))
			require.Len(t, listed.GetListings(), 1)
			assert.Equal(t, root, listed.GetListings()[0].GetPath())
		}
	}
}

// ---------------------------------------------------------------------------
// ListDirectory: the ancestor chain
// ---------------------------------------------------------------------------

// listChain and chainDirs, with no ResponseWriter and no registrar: the two
// rules that interleave in the handler -- the byte budget and "the outermost
// directory's failure is the request's failure" -- are decided here.

// The first listing survives a budget it alone exceeds. A reply with no
// listing is not an answer to the directory the caller asked about.
func TestListChain_KeepsTheFirstListingAboveTheBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	listings, _, err := listChain(context.Background(), []string{dir, child}, 1, 0, false)

	require.NoError(t, err)
	require.Len(t, listings, 1)
	assert.Equal(t, dir, listings[0].GetPath())
}

// A budget that fits both keeps both, so the test above cannot pass merely
// because the chain never grows.
func TestListChain_KeepsEveryListingThatFitsTheBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	listings, _, err := listChain(context.Background(), []string{dir, child}, 1<<20, 0, false)

	require.NoError(t, err)
	require.Len(t, listings, 2)
	assert.Equal(t, []string{dir, child}, []string{listings[0].GetPath(), listings[1].GetPath()})
}

// A caller that is already gone gets no walk and no listing.
func TestListChain_CancelledContextListsNothing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	listings, _, err := listChain(ctx, []string{t.TempDir()}, 1<<20, 0, false)

	assert.Nil(t, listings)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestListChain_OutermostFailureIsTheRequestFailure(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	listings, _, err := listChain(context.Background(), []string{missing}, 1<<20, 0, false)

	require.Error(t, err)
	assert.Nil(t, listings)
}

// The counterpart rule: a level below the outermost one only shortens the
// chain, so the caller keeps the levels that did list.
func TestListChain_DeeperFailureShortensTheChain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	listings, unreadable, err := listChain(context.Background(), []string{dir, missing}, 1<<20, 0, false)

	require.NoError(t, err)
	require.Len(t, listings, 1)
	assert.Equal(t, dir, listings[0].GetPath())

	// The chain says WHY it stopped, so a tree can report it on that row
	// instead of leaving a directory that silently refuses to open.
	require.NotNil(t, unreadable)
	assert.Equal(t, missing, unreadable.GetPath())
	assert.Equal(t, "no such directory", unreadable.GetReason())
}

// A cap is not a failure: the caller lists the rest itself, so there is
// nothing to report and nothing for a tree to render.
func TestListChain_ACapCutsTheChainWithNoReason(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	listings, unreadable, err := listChain(context.Background(), []string{dir, child}, 1, 0, false)

	require.NoError(t, err)
	require.Len(t, listings, 1)
	assert.Nil(t, unreadable)
}

// A complete chain reports nothing either.
func TestListChain_ACompleteChainHasNoUnreadableDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))

	listings, unreadable, err := listChain(context.Background(), []string{dir, child}, 1<<20, 0, false)

	require.NoError(t, err)
	require.Len(t, listings, 2)
	assert.Nil(t, unreadable)
}

// The reason is a fixed phrase, never the operating system's own message: that
// one carries the path, the errno spelling and the syscall name, and all three
// differ per platform.
func TestListChain_ReportsPermissionDeniedWithoutTheOsMessage(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not govern reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	require.NoError(t, os.MkdirAll(blocked, 0o755))
	require.NoError(t, os.Chmod(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, unreadable, err := listChain(context.Background(), []string{dir, blocked}, 1<<20, 0, false)

	require.NoError(t, err)
	require.NotNil(t, unreadable)
	assert.Equal(t, "permission denied", unreadable.GetReason())
	assert.NotContains(t, unreadable.GetReason(), blocked, "the reason must not carry the path")
}

// chainDirs is pure path algebra apart from one os.Stat on the tail, and that
// call leaves a path it cannot stat unchanged -- so every rule below is
// reachable with paths that do not exist.
func TestChainDirs(t *testing.T) {
	t.Parallel()
	// A prefix no test can collide with, and that no filesystem holds. The
	// spelling must be NATIVE: chainDirs derives every parent with
	// filepath.Dir, which cleans, so a POSIX literal compares unequal to its
	// own cleaned parent on Windows. The handler sanitizes both the path and
	// from_root before chainDirs sees them, so a mixed spelling never reaches
	// it outside this test.
	base := filepath.FromSlash("/leapmux-chaindirs-absent")

	t.Run("no root lists the path alone", func(t *testing.T) {
		t.Parallel()
		dirs, err := chainDirs(filepath.Join(base, "a", "b"), "")
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(base, "a", "b")}, dirs)
	})

	t.Run("a root walks down to the path, outermost first", func(t *testing.T) {
		t.Parallel()
		dirs, err := chainDirs(filepath.Join(base, "a", "b", "c"), base)
		require.NoError(t, err)
		assert.Equal(t, []string{
			base,
			filepath.Join(base, "a"),
			filepath.Join(base, "a", "b"),
			filepath.Join(base, "a", "b", "c"),
		}, dirs)
	})

	t.Run("a root equal to the path is a chain of one", func(t *testing.T) {
		t.Parallel()
		dirs, err := chainDirs(base, base)
		require.NoError(t, err)
		assert.Equal(t, []string{base}, dirs)
	})

	t.Run("a root that is not an ancestor is refused", func(t *testing.T) {
		t.Parallel()
		_, err := chainDirs(filepath.Join(base, "a"), filepath.Join(base, "b"))
		assert.Error(t, err)
	})

	// A sibling whose name merely starts with the root's is NOT under it.
	t.Run("a sibling with a shared name prefix is refused", func(t *testing.T) {
		t.Parallel()
		_, err := chainDirs(filepath.Join(base, "foobar"), filepath.Join(base, "foo"))
		assert.Error(t, err)
	})

	// The cap drops the DEEPEST levels: a caller renders from the root down,
	// and a chain missing its root renders nothing at all.
	t.Run("the cap keeps the outermost levels", func(t *testing.T) {
		t.Parallel()
		deep := base
		for i := 0; i < maxChainListings+10; i++ {
			deep = filepath.Join(deep, fmt.Sprintf("d%d", i))
		}
		dirs, err := chainDirs(deep, base)
		require.NoError(t, err)
		require.Len(t, dirs, maxChainListings)
		assert.Equal(t, base, dirs[0])
		assert.Equal(t, filepath.Join(base, "d0"), dirs[1])
	})
}

// listingPaths reads a ListDirectory reply and returns the path of each
// listing, in the order the worker sent them.
func listingPaths(t *testing.T, w *testResponseWriter) []string {
	t.Helper()
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	paths := make([]string, 0, len(resp.GetListings()))
	for _, l := range resp.GetListings() {
		paths = append(paths, l.GetPath())
	}
	return paths
}

func TestListDirectory_SingleListingWithoutFromRoot(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "a", "b"), 0o755))

	root := pathutil.Canonicalize(svc.HomeDir)
	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path: filepath.Join(root, "a"),
	}, w)

	assert.Equal(t, []string{filepath.Join(root, "a")}, listingPaths(t, w))
}

func TestListDirectory_ChainIsOutermostFirst(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "a", "b", "c"), 0o755))

	root := pathutil.Canonicalize(svc.HomeDir)
	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a", "b", "c"),
		FromRoot: root,
	}, w)

	assert.Equal(t, []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a", "b", "c"),
	}, listingPaths(t, w))
}

func TestListDirectory_ChainOfOneWhenFromRootEqualsPath(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: root, FromRoot: root}, w)

	assert.Equal(t, []string{root}, listingPaths(t, w))
}

func TestListDirectory_ChainRejectsAFromRootThatIsNotAnAncestor(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "a"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "b"), 0o755))
	root := pathutil.Canonicalize(svc.HomeDir)

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a"),
		FromRoot: filepath.Join(root, "b"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	assert.Empty(t, w.responses)
}

// A sibling whose name merely starts with the root's is NOT under it. This is
// the check HasPathPrefix exists for, and a plain strings.HasPrefix would pass
// it wrongly.
func TestListDirectory_ChainRejectsASiblingWithASharedNamePrefix(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "foo"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(svc.HomeDir, "foobar"), 0o755))
	root := pathutil.Canonicalize(svc.HomeDir)

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "foobar"),
		FromRoot: filepath.Join(root, "foo"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
}

// The worker canonicalizes `path`, so `from_root` must be canonicalized too
// before the containment test. Without that, a symlinked ancestor -- /tmp on
// macOS, which resolves to /private/tmp -- fails a check it should pass.
func TestListDirectory_ChainAcceptsASymlinkedFromRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevation on Windows")
	}
	svc, d, w := setupTestService(t)
	real := filepath.Join(svc.HomeDir, "real")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "a"), 0o755))
	link := filepath.Join(svc.HomeDir, "link")
	require.NoError(t, os.Symlink(real, link))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(link, "a"),
		FromRoot: link,
	}, w)

	canonical := pathutil.Canonicalize(real)
	assert.Equal(t, []string{canonical, filepath.Join(canonical, "a")}, listingPaths(t, w))
}

// Revealing a FILE costs one request too: the chain ends at the deepest
// directory the caller gave. Without this a tree that selects a file has to
// walk the chain itself, one level at a time.
func TestListDirectory_ChainEndsAtTheParentOfANonDirectory(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := filepath.Join(svc.HomeDir, "a", "b")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644))
	root := pathutil.Canonicalize(svc.HomeDir)

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a", "b", "f.txt"),
		FromRoot: root,
	}, w)

	assert.Equal(t, []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
	}, listingPaths(t, w))
}

// The chain form tolerates a file; the plain form still reports one as the
// error it is. A caller that asked to list a file without a from_root asked
// for something impossible.
func TestListDirectory_WithoutFromRootAFileIsStillAnError(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	path := filepath.Join(svc.HomeDir, "f.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: path}, w)

	require.Len(t, w.errors, 1)
	assert.Empty(t, w.responses)
}

// maxChainListings is the O(1) guard in front of the payload budget. It drops
// the DEEPEST levels, so the caller always receives the chain from its root
// down and can list the rest itself.
func TestListDirectory_ChainStopsAtTheListingCap(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)

	root := pathutil.Canonicalize(svc.HomeDir)
	deep := root
	for i := 0; i < maxChainListings+10; i++ {
		deep = filepath.Join(deep, fmt.Sprintf("d%d", i))
	}
	require.NoError(t, os.MkdirAll(deep, 0o755))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: deep, FromRoot: root}, w)

	paths := listingPaths(t, w)
	assert.Len(t, paths, maxChainListings)
	assert.Equal(t, root, paths[0], "the cap must drop the deepest levels, not the outermost")
	assert.Equal(t, filepath.Join(root, "d0"), paths[1])
}

// The budget stops the chain BEFORE the response outgrows what the channel
// accepts. The first listing is always kept: a reply with no listing is not an
// answer, and the caller asked about that directory.
func TestListDirectory_ChainStopsAtThePayloadBudget(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t, withMaxMessageSize(4096))

	root := pathutil.Canonicalize(svc.HomeDir)
	deep := root
	for i := 0; i < 8; i++ {
		deep = filepath.Join(deep, fmt.Sprintf("dir-%d", i))
		require.NoError(t, os.MkdirAll(deep, 0o755))
		// Enough siblings at every level that a few listings exhaust the
		// budget, so the cut is the budget's and not the depth cap's.
		for j := 0; j < 40; j++ {
			require.NoError(t, os.MkdirAll(filepath.Join(deep, fmt.Sprintf("sibling-with-a-long-name-%d", j)), 0o755))
		}
	}

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: deep, FromRoot: root}, w)

	paths := listingPaths(t, w)
	require.NotEmpty(t, paths)
	assert.Less(t, len(paths), 9, "the budget must cut the chain short")
	assert.Equal(t, root, paths[0], "the budget must drop the deepest levels, not the outermost")
}

// The chain that the directory picker actually issues: rooted at the host's
// own filesystem root, revealing a path under the home directory.
//
// The temp-directory roots above cannot catch a prefix test that mishandles a
// ROOT, because a temp directory is never one. This is the case that broke:
// HasPathPrefix appended a boundary separator unconditionally, so a root
// prefix became "//" and every descendant answered "not an ancestor".
func TestListDirectory_ChainFromTheFilesystemRoot(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	target := pathutil.Canonicalize(svc.HomeDir)

	roots := pathutil.FilesystemRoots()
	require.NotEmpty(t, roots)
	root := roots[0]
	if runtime.GOOS == "windows" {
		// The temp directory need not live on the first drive.
		root = filepath.VolumeName(target) + `\`
	}

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     target,
		FromRoot: root,
		DirsOnly: true,
	}, w)

	paths := listingPaths(t, w)
	require.NotEmpty(t, paths)
	assert.Equal(t, root, paths[0], "the chain must start at the filesystem root")
	assert.Equal(t, target, paths[len(paths)-1], "and end at the requested path")
}

// One rejection class, one status code. A path SanitizePath refuses is
// PermissionDenied whichever field carried it -- the same answer `path` has
// always given -- so a caller that branches on the code does not have to know
// which field it filled in.
func TestListDirectory_ChainRefusesAnUnsanitizableFromRoot(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	target := pathutil.Canonicalize(svc.HomeDir)

	for name, fromRoot := range map[string]string{
		"relative":  "not/absolute",
		"traversal": "/tmp/../etc",
		"empty-ish": "   ",
	} {
		t.Run(name, func(t *testing.T) {
			w := &testResponseWriter{channelID: testChannelID}
			dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
				Path:     target,
				FromRoot: fromRoot,
			}, w)

			require.Len(t, w.errors, 1)
			assert.Equal(t, codePermissionDenied, w.errors[0].code)
			assert.Empty(t, w.responses)

			// The SAME spelling in `path` answers the same way. This is the
			// property the split codes broke.
			pw := &testResponseWriter{channelID: testChannelID}
			dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: fromRoot}, pw)
			require.Len(t, pw.errors, 1)
			assert.Equal(t, w.errors[0].code, pw.errors[0].code,
				"one rejected spelling must not carry two status codes")
		})
	}
}

// A from_root that IS acceptable but is not an ancestor stays InvalidArgument.
// Nothing was denied there; the two arguments simply do not agree, and the
// caller can fix that.
func TestListDirectory_ChainRejectsAValidFromRootThatIsNotAnAncestor(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "b"), 0o755))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a"),
		FromRoot: filepath.Join(root, "b"),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
}

// A directory the caller cannot read only SHORTENS the chain. It is one the
// caller would have discovered on its own next request, and failing the whole
// reply would cost it the levels that did list.
func TestListDirectory_ChainStopsAtAnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not govern reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.MkdirAll(filepath.Join(blocked, "inner"), 0o755))
	require.NoError(t, os.Chmod(blocked, 0o000))
	// Restored so t.TempDir's own cleanup can remove the tree.
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(blocked, "inner"),
		FromRoot: root,
	}, w)

	assert.Equal(t, []string{root}, listingPaths(t, w),
		"the readable levels must survive an unreadable one below them")
}

// The mapper itself, including the branch a real filesystem is awkward to
// drive into: an error that is neither a refusal nor an absence.
func TestUnreadableReason(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "permission denied", unreadableReason(fs.ErrPermission))
	assert.Equal(t, "no such directory", unreadableReason(fs.ErrNotExist))
	assert.Equal(t, "cannot be read", unreadableReason(errors.New("i/o error")))

	// Wrapped, which is the shape os.ReadDir really returns.
	assert.Equal(t, "permission denied",
		unreadableReason(&fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}))
	assert.Equal(t, "no such directory",
		unreadableReason(fmt.Errorf("listing %q: %w", "/x", fs.ErrNotExist)))
}

// The phrase carries no path and no errno spelling: the path is already in the
// response, and the operating system's own message differs per platform.
func TestUnreadableReason_CarriesNoPathOrErrno(t *testing.T) {
	t.Parallel()

	reason := unreadableReason(&fs.PathError{Op: "open", Path: "/secret/place", Err: fs.ErrPermission})

	assert.NotContains(t, reason, "/secret/place")
	assert.NotContains(t, reason, "open")
}

// The reason reaches the caller through the RPC, not only through listChain.
func TestListDirectory_ChainReportsTheUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not govern reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	blocked := filepath.Join(root, "blocked")
	require.NoError(t, os.MkdirAll(filepath.Join(blocked, "inner"), 0o755))
	require.NoError(t, os.Chmod(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(blocked, "inner"),
		FromRoot: root,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetListings(), 1)
	require.NotNil(t, resp.GetUnreadable())
	assert.Equal(t, blocked, resp.GetUnreadable().GetPath())
	assert.Equal(t, "permission denied", resp.GetUnreadable().GetReason())
}

// A chain that completes carries no reason, so a tree cannot render a stale
// one over a directory that lists perfectly well.
func TestListDirectory_ACompleteChainReportsNoUnreadableDirectory(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a", "b"),
		FromRoot: root,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Nil(t, resp.GetUnreadable())
}

// The FIRST directory is the one the caller asked about, so its failure is the
// request's failure -- the counterpart to the rule above.
func TestListDirectory_ChainFailsWhenTheRootIsUnreadable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions do not govern reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	svc, d, w := setupTestService(t)
	root := filepath.Join(pathutil.Canonicalize(svc.HomeDir), "blocked")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "inner"), 0o755))
	require.NoError(t, os.Chmod(root, 0o000))
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "inner"),
		FromRoot: root,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Empty(t, w.responses)
}

// The truncation fields moved onto DirectoryListing, and every test that
// covers them calls the internal helper directly -- so nothing pinned the
// wiring from listDirectory's return values onto the message the caller
// receives. A crossed or dropped field there removes the tree's truncation
// notice, or makes it claim "N+ entries" for every truncated directory, with
// no other symptom.
func TestListDirectory_CarriesTruncationOnTheListing(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	dir := filepath.Join(pathutil.Canonicalize(svc.HomeDir), "big")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const total = maxDirEntries + 25
	for i := 0; i < total; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644))
	}

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: dir}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetListings(), 1)
	listing := resp.GetListings()[0]
	assert.True(t, listing.GetTruncated(), "a directory above the entry limit is truncated")
	assert.Len(t, listing.GetEntries(), maxDirEntries)
	assert.EqualValues(t, total, listing.GetTotalEntries(), "total_entries reports what the directory really held")
}

// The same fields, on a DEEPER listing of a chain. The re-key the caller
// applies touches the first listing alone, so a chain that carried the flags
// only there would leave every other level unable to report its own
// truncation.
func TestListDirectory_ChainCarriesTruncationOnADeeperListing(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	dir := filepath.Join(root, "big")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const total = maxDirEntries + 5
	for i := 0; i < total; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644))
	}

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{Path: dir, FromRoot: root}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetListings(), 2)
	assert.False(t, resp.GetListings()[0].GetTruncated(), "the root holds one entry")
	last := resp.GetListings()[1]
	assert.Equal(t, dir, last.GetPath())
	assert.True(t, last.GetTruncated())
	assert.EqualValues(t, total, last.GetTotalEntries())
}

// max_depth WITH from_root -- the only combination the directory picker ever
// sends, and the one no test covered. The merge rewrites an entry's name and
// path to its deepest single child, so a chain member can arrive labelled after a
// directory several levels below it.
func TestListDirectory_ChainAppliesMaxDepthToEveryListing(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	// One single-child spine under the root, plus a sibling at the root so the
	// root itself has more than one entry and is not merged away.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "other"), 0o755))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(root, "a", "b", "c"),
		FromRoot: root,
		MaxDepth: 5,
		DirsOnly: true,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// Every level of the chain is still listed, whatever the merge did to the
	// names inside each listing. The caller keys its cache by these paths, so
	// a missing one is a level it can never fill.
	var paths []string
	for _, l := range resp.GetListings() {
		paths = append(paths, l.GetPath())
	}
	assert.Equal(t, []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a", "b", "c"),
	}, paths)

	// The merge collapses the single-child spine into one entry, so the row
	// the root offers points at a directory several levels down. Its PATH is the
	// deepest merged directory, which is what the caller opens.
	rootEntries := resp.GetListings()[0].GetEntries()
	var merged *leapmuxv1.FileInfo
	for _, e := range rootEntries {
		if strings.HasPrefix(e.GetName(), "a") {
			merged = e
		}
	}
	require.NotNil(t, merged, "the root must still offer the spine")
	assert.Equal(t, filepath.Join(root, "a", "b", "c"), merged.GetPath(),
		"a merged entry points at the deepest directory it collapsed")
}

// Every listing in a chain is shaped like the single-listing answer. Without
// this, a chain could quietly ignore dirs_only and hand a directory picker the
// files it asked not to receive.
func TestListDirectory_ChainAppliesDirsOnlyToEveryListing(t *testing.T) {
	t.Parallel()
	svc, d, w := setupTestService(t)
	root := pathutil.Canonicalize(svc.HomeDir)
	mid := filepath.Join(root, "mid")
	require.NoError(t, os.MkdirAll(filepath.Join(mid, "leaf"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "root-file.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mid, "mid-file.txt"), []byte("x"), 0o644))

	dispatch(d, "ListDirectory", &leapmuxv1.ListDirectoryRequest{
		Path:     filepath.Join(mid, "leaf"),
		FromRoot: root,
		DirsOnly: true,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListDirectoryResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetListings(), 3)

	for _, listing := range resp.GetListings() {
		for _, e := range listing.GetEntries() {
			assert.True(t, e.GetIsDir(), "listing %q returned a file: %s", listing.GetPath(), e.GetName())
		}
	}
}
