package service

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Error("expected truncated=false for 10 entries")
		}
		if len(entries) != 10 {
			t.Errorf("expected 10 entries, got %d", len(entries))
		}
	})

	t.Run("exactly at limit is not truncated", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < maxDirEntries; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		entries, truncated, err := listDirectory(dir, dir, 0, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Errorf("expected truncated=false for exactly %d entries", maxDirEntries)
		}
		if len(entries) != maxDirEntries {
			t.Errorf("expected %d entries, got %d", maxDirEntries, len(entries))
		}
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
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Errorf("expected truncated=true for %d entries", total)
		}
		if len(entries) != maxDirEntries {
			t.Errorf("expected %d entries, got %d", maxDirEntries, len(entries))
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}

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

	if len(entries) != len(expected) {
		t.Fatalf("expected %d entries, got %d", len(expected), len(entries))
	}
	for i, want := range expected {
		if entries[i].Name != want.name {
			t.Errorf("entry[%d].Name = %q, want %q", i, entries[i].Name, want.name)
		}
		if entries[i].IsDir != want.isDir {
			t.Errorf("entry[%d].IsDir = %v, want %v", i, entries[i].IsDir, want.isDir)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len(entries) != maxDirEntries {
		t.Fatalf("expected %d entries, got %d", maxDirEntries, len(entries))
	}

	// All 100 directories should appear before any files.
	dirCount := 0
	for i, e := range entries {
		if e.IsDir {
			dirCount++
		} else if dirCount < numDirs {
			t.Errorf("file %q appeared at index %d before all directories", e.Name, i)
			break
		}
	}
	if dirCount != numDirs {
		t.Errorf("expected %d directories, got %d", numDirs, dirCount)
	}

	// The remaining entries should be files in alphabetical order.
	fileEntries := entries[numDirs:]
	for i := 1; i < len(fileEntries); i++ {
		if fileEntries[i].Name < fileEntries[i-1].Name {
			t.Errorf("files not sorted: %q < %q at index %d", fileEntries[i].Name, fileEntries[i-1].Name, numDirs+i)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	regularInfo, err := os.Stat(regularPath)
	if err != nil {
		t.Fatal(err)
	}

	hiddenProto := fileInfoToProto(hiddenInfo, hiddenPath)
	if !hiddenProto.Hidden {
		t.Errorf("expected Hidden=true for %q", hiddenPath)
	}

	regularProto := fileInfoToProto(regularInfo, regularPath)
	if regularProto.Hidden {
		t.Errorf("expected Hidden=false for %q", regularPath)
	}
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
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		expectHidden := e.Name[0] == '.'
		if e.Hidden != expectHidden {
			t.Errorf("entry %q: Hidden=%v, want %v", e.Name, e.Hidden, expectHidden)
		}
	}
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
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Error("expected truncated=false")
		}
		if len(entries) != numDirs {
			t.Errorf("expected %d entries, got %d", numDirs, len(entries))
		}
		for _, e := range entries {
			if !e.IsDir {
				t.Errorf("expected only directories, got file %q", e.Name)
			}
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
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Error("expected truncated=true")
		}
		if len(entries) != maxDirEntries {
			t.Errorf("expected %d entries, got %d", maxDirEntries, len(entries))
		}
		for _, e := range entries {
			if !e.IsDir {
				t.Errorf("expected only directories, got file %q", e.Name)
			}
		}
	})
}
