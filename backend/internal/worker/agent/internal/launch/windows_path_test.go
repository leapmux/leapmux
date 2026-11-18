package launch

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsPathRefreshAppliesSavedChanges(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\Windows", "C:\\Old", "C:\\Keep", "C:\\Excluded"}
	originalSaved := saved
	var readErr error
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, readErr })
	inherited := []string{"C:\\Launcher", "c:\\windows", "C:/Old/", "C:\\Injected", "C:\\Keep", "C:\\Tail"}

	assert.Equal(t, inherited, windowsPathDirectories(t, refresher, inherited), "preserve the exact launch PATH before a saved change")
	saved = []string{"C:\\Windows", "C:\\New", "C:\\Keep", "C:\\Excluded"}
	want := []string{"C:\\Launcher", "c:\\windows", "C:\\Injected", "C:\\Keep", "C:\\Tail", "C:\\New"}
	assert.Equal(t, want, windowsPathDirectories(t, refresher, inherited))
	assert.Equal(t, []string{"C:\\Launcher", "c:\\windows", "C:/Old/", "C:\\Injected", "C:\\Keep", "C:\\Tail"}, inherited)

	readErr = errors.New("Windows environment block unavailable")
	last, err := refresher.directories(inherited)
	require.ErrorIs(t, err, readErr)
	assert.Equal(t, want, last, "keep the latest successful Windows read")
	readErr = nil
	saved = originalSaved
	assert.Equal(t, inherited, windowsPathDirectories(t, refresher, inherited), "reverting the saved change restores the original launch PATH")
}

func TestWindowsPathRefreshHandlesEmptySavedPath(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\Windows"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })

	saved = nil
	assert.Equal(t, []string{"C:\\Launcher"}, windowsPathDirectories(t, refresher, []string{"C:\\Launcher", "C:\\Windows"}))
	saved = []string{"C:\\New"}
	assert.Equal(t, []string{"C:\\Launcher", "C:\\New"}, windowsPathDirectories(t, refresher, []string{"C:\\Launcher", "C:\\Windows"}))
}

func TestWindowsPathRefreshPreservesLauncherPrecedence(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\System", "C:\\User"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	inherited := []string{"C:\\Launcher", "C:\\System", "C:\\User", "C:\\Tail"}

	saved = []string{"C:\\User", "C:\\System", "c:\\launcher"}
	assert.Equal(t, []string{"C:\\Launcher", "C:\\User", "C:\\System", "c:\\launcher", "C:\\Tail"}, windowsPathDirectories(t, refresher, inherited))
}

func TestWindowsPathRefreshRecoversFromInitialReadFailure(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\New"}
	readErr := errors.New("Windows environment block unavailable")
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, readErr })
	inherited := []string{"C:\\Launcher", "C:\\System"}

	directories, err := refresher.directories(inherited)
	require.ErrorIs(t, err, readErr)
	assert.Equal(t, inherited, directories)
	readErr = nil
	assert.Equal(t, []string{"C:\\Launcher", "C:\\System", "C:\\New"}, windowsPathDirectories(t, refresher, inherited))
}

func TestWindowsPathRefreshPreservesExtraOccurrenceOfRemovedDirectory(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\System", "C:\\Tools"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	inherited := []string{"C:\\Tools", "C:\\System", "C:\\Tools"}
	saved = []string{"C:\\System"}
	assert.Equal(t, []string{"C:\\Tools", "C:\\System"}, windowsPathDirectories(t, refresher, inherited))
}

func TestWindowsPathRefreshPreservesAnAppendedDuplicate(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\Tools", "C:\\System"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	inherited := []string{"C:\\Tools", "C:\\System", "C:\\Tools"}

	saved = []string{"C:\\System"}
	assert.Equal(t, []string{"C:\\System", "C:\\Tools"}, windowsPathDirectories(t, refresher, inherited))
}

func TestWindowsPathRefreshPreservesCustomizedOrder(t *testing.T) {
	t.Parallel()
	saved := []string{"C:\\System", "C:\\User", "C:\\System", "C:\\Other"}
	refresher := newWindowsPathRefresh(func() ([]string, error) { return saved, nil })
	inherited := []string{"C:\\User", "C:\\System", "C:\\System", "C:\\Injected", "C:\\Other"}

	saved = append(saved, "C:\\New")
	assert.Equal(t, append(slices.Clone(inherited), "C:\\New"), windowsPathDirectories(t, refresher, inherited))
}

func windowsPathDirectories(t *testing.T, refresher *windowsPathRefresh, inherited []string) []string {
	t.Helper()
	directories, err := refresher.directories(inherited)
	require.NoError(t, err)
	return directories
}
