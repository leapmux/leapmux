package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProber lets tests describe a "what's installed" world without touching
// the real filesystem. paths is the set of absolute paths that resolve.
// pathInfos lets a test mark a path as a directory (default: regular file).
type fakeProber struct {
	paths     map[string]bool
	pathInfos map[string]os.FileMode
	pathLook  map[string]string
	home      string
	env       map[string]string
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		paths:     map[string]bool{},
		pathInfos: map[string]os.FileMode{},
		pathLook:  map[string]string{},
		home:      "/home/u",
		env:       map[string]string{},
	}
}

func (f *fakeProber) addPath(p string)        { f.paths[p] = true }
func (f *fakeProber) addLookPath(n, p string) { f.pathLook[n] = p }
func (f *fakeProber) setEnv(k, v string)      { f.env[k] = v }
func (f *fakeProber) setHome(h string)        { f.home = h }

func (f *fakeProber) Stat(p string) (os.FileInfo, error) {
	if !f.paths[p] {
		return nil, fs.ErrNotExist
	}
	mode := f.pathInfos[p]
	return fakeFileInfo{name: filepath.Base(p), mode: mode}, nil
}

func (f *fakeProber) LookPath(n string) (string, error) {
	p, ok := f.pathLook[n]
	if !ok {
		return "", errors.New("not found")
	}
	return p, nil
}

func (f *fakeProber) Glob(pattern string) ([]string, error) {
	var out []string
	for p := range f.paths {
		ok, _ := filepath.Match(pattern, p)
		if ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeProber) Home() string        { return f.home }
func (f *fakeProber) Env(n string) string { return f.env[n] }

type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

// recordingLauncher captures launch attempts without actually exec'ing.
type recordingLauncher struct {
	calls []launchCall
	err   error
}

type launchCall struct {
	kind execKind
	path string
	dir  string
}

func (r *recordingLauncher) Launch(d *detectedExec, dir string) error {
	r.calls = append(r.calls, launchCall{kind: d.kind, path: d.path, dir: dir})
	return r.err
}

// --- Detection: composability ---

func TestTryAll_FirstCandidateWins(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("first", "/usr/bin/first")
	p.addLookPath("second", "/usr/bin/second")

	got := tryAll(tryLookPath("first"), tryLookPath("second"))(p)
	require.NotNil(t, got)
	assert.Equal(t, "/usr/bin/first", got.path)
}

func TestTryAll_FallsThroughMissing(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("second", "/usr/bin/second")

	got := tryAll(tryLookPath("first"), tryLookPath("second"))(p)
	require.NotNil(t, got)
	assert.Equal(t, "/usr/bin/second", got.path)
}

func TestTryAll_NoMatchReturnsNil(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	got := tryAll(tryLookPath("nope"), tryPath("/missing"))(p)
	assert.Nil(t, got)
}

func TestTryPath_ExpandsHomeAndEnv(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/home/alice")
	p.setEnv("LOCALAPPDATA", `C:\Users\alice\AppData\Local`)
	p.addPath("/home/alice/Library/Application Support/JetBrains/Toolbox/scripts/idea")
	p.addPath(`C:\Users\alice\AppData\Local\Programs\Microsoft VS Code\bin\code.cmd`)

	got1 := tryPath("~/Library/Application Support/JetBrains/Toolbox/scripts/idea")(p)
	require.NotNil(t, got1)

	got2 := tryPath(`%LOCALAPPDATA%\Programs\Microsoft VS Code\bin\code.cmd`)(p)
	require.NotNil(t, got2)
}

func TestTryPath_UnresolvedEnvReturnsNil(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	// %FOO% is not set in env → expandPath should return "" → tryPath gives nil.
	got := tryPath(`%FOO%\thing.exe`)(p)
	assert.Nil(t, got)
}

func TestTryGlob_PicksLastMatchAsHighestVersion(t *testing.T) {
	t.Parallel()
	// Use POSIX-style paths so filepath.Match's per-OS separator semantics
	// don't change the test outcome between dev hosts.
	p := newFakeProber()
	p.setEnv("OPT", "/opt")
	p.addPath("/opt/JetBrains/IntelliJ-IDEA-Ultimate-2023.3/bin/idea")
	p.addPath("/opt/JetBrains/IntelliJ-IDEA-Ultimate-2024.2/bin/idea")

	got := tryGlob("$OPT/JetBrains/IntelliJ-IDEA-Ultimate-*/bin/idea")(p)
	require.NotNil(t, got)
	assert.Contains(t, got.path, "2024.2")
}

func TestTryMacOSApp_ProbesBothApplicationsRoots(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	// Only the user-Applications copy exists.
	p.addPath("/Users/alice/Applications/Visual Studio Code.app")

	got := tryMacOSApp("Visual Studio Code")(p)
	require.NotNil(t, got)
	assert.Equal(t, execKindMacOSApp, got.kind)
	assert.Equal(t, "Visual Studio Code", got.path, "path field carries bundle name")
}

// --- Registry behavior ---

func TestRegistry_OnlyDetectedEditorsAreListed(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")

	r := newEditorRegistry([]EditorSpec{
		{ID: "vscode", DisplayName: "Visual Studio Code", detect: tryLookPath("code")},
		{ID: "ghost", DisplayName: "Ghost Editor", detect: tryLookPath("ghost")},
	}, p, &recordingLauncher{})

	got := r.List()
	require.Len(t, got, 1)
	assert.Equal(t, "vscode", got[0].ID)
}

func TestRegistry_DetectionIsCached(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")

	calls := 0
	spec := EditorSpec{
		ID: "vscode", DisplayName: "VS Code",
		detect: func(pp Prober) *detectedExec {
			calls++
			return tryLookPath("code")(pp)
		},
	}
	r := newEditorRegistry([]EditorSpec{spec}, p, &recordingLauncher{})

	_ = r.List()
	_ = r.List()
	_ = r.List()
	assert.Equal(t, 1, calls, "detect should run once across many List calls")
}

func TestRegistry_RefreshReprobes(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")

	calls := 0
	spec := EditorSpec{
		ID: "vscode", DisplayName: "VS Code",
		detect: func(pp Prober) *detectedExec {
			calls++
			return tryLookPath("code")(pp)
		},
	}
	r := newEditorRegistry([]EditorSpec{spec}, p, &recordingLauncher{})

	_ = r.List()
	_ = r.Refresh()
	_ = r.Refresh()
	assert.Equal(t, 3, calls, "Refresh must always re-run detect, even after List has cached")
}

func TestRegistry_RefreshReflectsNewlyInstalledEditor(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	// Initial state: nothing is installed.
	specs := []EditorSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}
	r := newEditorRegistry(specs, p, &recordingLauncher{})

	assert.Empty(t, r.List(), "no editors detected initially")

	// User installs VS Code while LeapMux is running.
	p.addLookPath("code", "/usr/bin/code")

	assert.Empty(t, r.List(), "stale cache must not pick up the install on its own")
	got := r.Refresh()
	require.Len(t, got, 1)
	assert.Equal(t, "vscode", got[0].ID)
}

func TestRegistry_RefreshReflectsUninstalledEditor(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")
	specs := []EditorSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}
	r := newEditorRegistry(specs, p, &recordingLauncher{})

	require.Len(t, r.List(), 1)

	// User uninstalls VS Code.
	delete(p.pathLook, "code")

	got := r.Refresh()
	assert.Empty(t, got, "Refresh must surface an uninstall")
}

func TestRegistry_OpenLaunchesDetectedExec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")
	launcher := &recordingLauncher{}
	r := newEditorRegistry([]EditorSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}, p, launcher)

	require.NoError(t, r.Open("vscode", dir))
	require.Len(t, launcher.calls, 1)
	assert.Equal(t, "/usr/bin/code", launcher.calls[0].path)
	assert.Equal(t, dir, launcher.calls[0].dir)
}

func TestRegistry_OpenUnknownEditor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := newEditorRegistry(nil, newFakeProber(), &recordingLauncher{})

	err := r.Open("nope", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestRegistry_OpenWrapsLauncherError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")
	launcher := &recordingLauncher{err: errors.New("boom")}
	r := newEditorRegistry([]EditorSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}, p, launcher)

	err := r.Open("vscode", dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VS Code")
	assert.Contains(t, err.Error(), "boom")
}

// --- Path validation ---

func TestValidateOpenPath_RejectsEmpty(t *testing.T) {
	t.Parallel()
	_, err := validateOpenPath("")
	require.Error(t, err)
}

func TestValidateOpenPath_RejectsRelative(t *testing.T) {
	t.Parallel()
	_, err := validateOpenPath("./relative")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestValidateOpenPath_RejectsTraversal(t *testing.T) {
	t.Parallel()
	_, err := validateOpenPath("/etc/../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traversal")
}

func TestValidateOpenPath_RejectsMissing(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	_, err := validateOpenPath(missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not accessible")
}

func TestValidateOpenPath_RejectsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err := validateOpenPath(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestValidateOpenPath_AcceptsDirectory(t *testing.T) {
	t.Parallel()
	cleaned, err := validateOpenPath(t.TempDir())
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(cleaned))
}

// --- Spec table sanity (per OS, via the actual defaultEditorSpecs()) ---

func TestDefaultEditorSpecs_IDsUniqueAndNonEmpty(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		assert.NotEmpty(t, spec.ID, "every spec must have an ID")
		assert.NotEmpty(t, spec.DisplayName, "every spec must have a DisplayName: %s", spec.ID)
		assert.NotNil(t, spec.detect, "every spec must have a detect func: %s", spec.ID)
		assert.False(t, seen[spec.ID], "duplicate spec ID: %s", spec.ID)
		// Stable id format: lowercase, kebab.
		assert.Equal(t, strings.ToLower(spec.ID), spec.ID, "spec ID must be lowercase: %s", spec.ID)
		seen[spec.ID] = true
	}
}

func TestDefaultEditorSpecs_CoversCoreSet(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		seen[spec.ID] = true
	}
	// These editors must exist on EVERY OS we ship.
	for _, must := range []string{
		"vscode", "vscode-insiders", "vscodium", "cursor", "windsurf",
		"sublime-text", "zed",
		"intellij-idea-ultimate", "intellij-idea-community",
		"webstorm", "goland", "rustrover",
		"pycharm-professional", "pycharm-community",
		"phpstorm", "rubymine", "clion", "rider", "datagrip",
		"android-studio", "fleet",
	} {
		assert.True(t, seen[must], "core editor missing from registry: %s", must)
	}
}
