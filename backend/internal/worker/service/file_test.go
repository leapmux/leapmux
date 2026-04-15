package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListDirectory_Truncation(t *testing.T) {
	t.Run("below limit is not truncated", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 10; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
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

		entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
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

		entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
		require.NoError(t, err)
		assert.True(t, truncated, "expected truncated=true for %d entries", total)
		assert.Len(t, entries, maxDirEntries)
	})
}

func TestListDirectory_SortOrder(t *testing.T) {
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

	entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
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

func TestListDirectory_TruncationKeepsDirsFirst(t *testing.T) {
	dir := t.TempDir()

	// Create enough directories and files to exceed the limit.
	// 100 directories + 100 files = 200 > 128.
	// After truncation, all 100 dirs should be kept, plus 28 files.
	numDirs := 100
	numFiles := 100
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

	entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
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

	entries, _, err := listDirectory(dir, dir, 0, 0, false)
	require.NoError(t, err)

	for _, e := range entries {
		expectHidden := e.Name[0] == '.'
		assert.Equal(t, expectHidden, e.Hidden, "entry %q: Hidden", e.Name)
	}
}

func TestListDirectory_MergeHiddenDirs(t *testing.T) {
	t.Run("hidden top-level dir is merged with hidden flag", func(t *testing.T) {
		dir := t.TempDir()
		// .github/workflows — hidden dir should be merged, with hidden flag propagated.
		if err := os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		expected := filepath.Join(".github", "workflows")
		assert.Equal(t, expected, entries[0].Name)
		assert.True(t, entries[0].Hidden, "expected Hidden=true for merged .github/workflows")
	})

	t.Run("hidden child propagates hidden flag through merge", func(t *testing.T) {
		dir := t.TempDir()
		// src/.internal/utils — merge should go through .internal, propagating hidden.
		if err := os.MkdirAll(filepath.Join(dir, "src", ".internal", "utils"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		expected := filepath.Join("src", ".internal", "utils")
		assert.Equal(t, expected, entries[0].Name)
		assert.True(t, entries[0].Hidden, "expected Hidden=true when a hidden dir is in the merged path")
	})

	t.Run("non-hidden single-child dirs merge without hidden flag", func(t *testing.T) {
		dir := t.TempDir()
		// src/main/java — all visible, should merge normally, not hidden.
		if err := os.MkdirAll(filepath.Join(dir, "src", "main", "java"), 0o755); err != nil {
			t.Fatal(err)
		}

		entries, _, err := listDirectory(dir, dir, 5, 0, false)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		expected := filepath.Join("src", "main", "java")
		assert.Equal(t, expected, entries[0].Name)
		assert.False(t, entries[0].Hidden, "expected Hidden=false for non-hidden merged path")
	})
}

func TestListDirectory_DirsOnly(t *testing.T) {
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

		entries, truncated, err := listDirectory(dir, dir, 0, 0, true)
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

		entries, truncated, err := listDirectory(dir, dir, 0, 0, true)
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

		entries, _, err := listDirectory(dir, dir, 0, 0, true)
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

		entries, _, err := listDirectory(dir, dir, 0, 0, false)
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
