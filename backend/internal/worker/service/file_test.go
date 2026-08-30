package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
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
	maxRead := svc.maxReadLimit(nil)
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

func TestMaxReadLimit_UsesConfiguredMaxMessageSize(t *testing.T) {
	t.Parallel()

	svc := &Service{Config: Config{MaxMessageSize: 2 << 20}}
	assert.Equal(t, int64(2<<20), svc.maxReadLimit(nil))

	svc.MaxMessageSize = 0
	assert.Equal(t, int64(contracts.MaxMessageSize), svc.maxReadLimit(nil),
		"0 must resolve to the protocol default payload budget")
}

func TestMaxReadLimit_UsesNegotiatedChannelBudgetWhenTighter(t *testing.T) {
	t.Parallel()

	svc := &Service{Config: Config{MaxMessageSize: 4 << 20}}
	sender := &budgetWriter{budget: 1 << 20}
	assert.Equal(t, int64(1<<20), svc.maxReadLimit(sender),
		"channel negotiated budget must clamp below the worker knob")
	assert.Equal(t, int64(4<<20), svc.maxReadLimit(&budgetWriter{budget: 8 << 20}),
		"worker knob must win when the channel budget is higher")
	assert.Equal(t, int64(4<<20), svc.maxReadLimit(&budgetWriter{budget: 0}),
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
