package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
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

// All path keys are stored canonicalized to forward slashes so tests can
// describe their world POSIX-style and still match on Windows, where
// production code's filepath.Join introduces backslashes that wouldn't
// otherwise hit our map.
func (f *fakeProber) addPath(p string)        { f.paths[filepath.ToSlash(p)] = true }
func (f *fakeProber) addLookPath(n, p string) { f.pathLook[n] = p }
func (f *fakeProber) setEnv(k, v string)      { f.env[k] = v }
func (f *fakeProber) setHome(h string)        { f.home = h }

func (f *fakeProber) Stat(p string) (os.FileInfo, error) {
	key := filepath.ToSlash(p)
	if !f.paths[key] {
		return nil, fs.ErrNotExist
	}
	mode := f.pathInfos[key]
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
	pattern = filepath.ToSlash(pattern)
	var out []string
	for p := range f.paths {
		ok, _ := path.Match(pattern, p)
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
	assert.Equal(t, "/Users/alice/Applications/Visual Studio Code.app", filepath.ToSlash(got.path),
		"path field carries the RESOLVED bundle, so `open -a` addresses one exact copy")
}

func TestTryMacOSApp_PrefersSystemApplicationsOverUserCopy(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addPath("/Applications/Cursor.app")
	p.addPath("/Users/alice/Applications/Cursor.app")

	got := tryMacOSApp("Cursor")(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Applications/Cursor.app", filepath.ToSlash(got.path))
}

// JetBrains Toolbox installs one level below ~/Applications. Without that base
// a Toolbox user falls through to the wrapper script, which starts the IDE
// without raising it.
func TestTryMacOSApp_FindsJetBrainsToolboxBundle(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addPath("/Users/alice/Applications/JetBrains Toolbox/GoLand.app")

	got := tryMacOSApp("GoLand")(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Users/alice/Applications/JetBrains Toolbox/GoLand.app", filepath.ToSlash(got.path))
}

// One product, two bundle names: the website's download and the Toolbox copy
// differ, and Zed ships a Preview channel beside the stable one.
func TestTryMacOSApp_AcceptsAnyOfSeveralBundleNames(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addPath("/Applications/Zed Preview.app")

	got := tryMacOSApp("Zed", "Zed Preview")(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Applications/Zed Preview.app", filepath.ToSlash(got.path))
}

// A "~" base with no home directory would otherwise expand to a RELATIVE path
// and probe whatever the working directory happens to be.
func TestTryMacOSApp_SkipsRelativeCandidateWhenHomeIsEmpty(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("")
	p.addPath("Applications/Cursor.app")

	assert.Nil(t, tryMacOSApp("Cursor")(p))
}

// --- Registry behavior ---

func TestRegistry_OnlyDetectedAppsAreListed(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")

	r := newExternalAppRegistry([]ExternalAppSpec{
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
	spec := ExternalAppSpec{
		ID: "vscode", DisplayName: "VS Code",
		detect: func(pp Prober) *detectedExec {
			calls++
			return tryLookPath("code")(pp)
		},
	}
	r := newExternalAppRegistry([]ExternalAppSpec{spec}, p, &recordingLauncher{})

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
	spec := ExternalAppSpec{
		ID: "vscode", DisplayName: "VS Code",
		detect: func(pp Prober) *detectedExec {
			calls++
			return tryLookPath("code")(pp)
		},
	}
	r := newExternalAppRegistry([]ExternalAppSpec{spec}, p, &recordingLauncher{})

	_ = r.List()
	_ = r.Refresh()
	_ = r.Refresh()
	assert.Equal(t, 3, calls, "Refresh must always re-run detect, even after List has cached")
}

func TestRegistry_RefreshReflectsNewlyInstalledEditor(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	// Initial state: nothing is installed.
	specs := []ExternalAppSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}
	r := newExternalAppRegistry(specs, p, &recordingLauncher{})

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
	specs := []ExternalAppSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
	}
	r := newExternalAppRegistry(specs, p, &recordingLauncher{})

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
	r := newExternalAppRegistry([]ExternalAppSpec{
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
	r := newExternalAppRegistry(nil, newFakeProber(), &recordingLauncher{})

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
	r := newExternalAppRegistry([]ExternalAppSpec{
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
	// Build an absolute path containing ".." that is absolute on both
	// POSIX and Windows. A literal "/etc/..." is drive-relative on
	// Windows and would fail the IsAbs check before the traversal check
	// runs. Avoid filepath.Join, which would Clean and collapse the "..".
	base := t.TempDir()
	input := base + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(base)
	_, err := validateOpenPath(input)
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

// --- Spec table sanity (per OS, via the actual defaultExternalAppSpecs()) ---

func TestDefaultExternalAppSpecs_IDsUniqueAndNonEmpty(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, spec := range defaultExternalAppSpecs() {
		assert.NotEmpty(t, spec.ID, "every spec must have an ID")
		assert.NotEmpty(t, spec.DisplayName, "every spec must have a DisplayName: %s", spec.ID)
		assert.NotNil(t, spec.detect, "every spec must have a detect func: %s", spec.ID)
		assert.False(t, seen[spec.ID], "duplicate spec ID: %s", spec.ID)
		// Stable id format: lowercase, kebab.
		assert.Equal(t, strings.ToLower(spec.ID), spec.ID, "spec ID must be lowercase: %s", spec.ID)
		seen[spec.ID] = true
	}
}

// The spec table for this OS must hold exactly the ids the contract lists for
// it. This replaces a hand-written "core set" that named a subset and trusted
// review for the rest, so an id added on one platform only, or dropped from
// one, now fails here instead of silently losing its icon in the browser.
func TestDefaultExternalAppSpecs_MatchTheContractForThisOS(t *testing.T) {
	t.Parallel()
	want := contracts.ExternalAppIDsByOS[runtime.GOOS]
	require.NotEmpty(t, want, "contracts/external-apps.json lists no app for %s", runtime.GOOS)

	var got []string
	for _, spec := range defaultExternalAppSpecs() {
		got = append(got, spec.ID)
	}
	assert.ElementsMatch(t, want, got)
}

// Every spec's kind resolves, so nothing reaches the browser as the unset
// value -- the app menu groups by kind, and an unset one lands in no group.
func TestDefaultExternalAppSpecs_EveryIDHasAKind(t *testing.T) {
	t.Parallel()
	for _, spec := range defaultExternalAppSpecs() {
		kind := contracts.ExternalAppKindByID[spec.ID]
		assert.NotEqual(t, desktoppb.ExternalAppKind_EXTERNAL_APP_KIND_UNSPECIFIED, kind,
			"spec %s has no contract kind", spec.ID)
	}
}

// The file manager is the one app that needs no probe, and the app menu counts
// on it: it renders that kind as an always-present group ahead of the editors.
func TestDefaultExternalAppSpecs_FileManagerIsAlwaysDetected(t *testing.T) {
	t.Parallel()
	r := newExternalAppRegistry(defaultExternalAppSpecs(), newFakeProber(), &recordingLauncher{})

	var found *ExternalApp
	for _, app := range r.List() {
		if app.ID == fileManagerID {
			found = &app
			break
		}
	}
	require.NotNil(t, found, "the file manager must be detected on a machine with nothing installed")
	assert.Equal(t, desktoppb.ExternalAppKind_EXTERNAL_APP_KIND_FILE_MANAGER, found.Kind)
	assert.NotEmpty(t, found.DisplayName)
}

func TestRegistry_StampsTheContractKindOnEveryApp(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("code", "/usr/bin/code")
	r := newExternalAppRegistry([]ExternalAppSpec{
		{ID: "vscode", DisplayName: "VS Code", detect: tryLookPath("code")},
		fileManagerSpec("Finder"),
	}, p, &recordingLauncher{})

	got := r.List()
	require.Len(t, got, 2)
	assert.Equal(t, desktoppb.ExternalAppKind_EXTERNAL_APP_KIND_EDITOR, got[0].Kind)
	assert.Equal(t, desktoppb.ExternalAppKind_EXTERNAL_APP_KIND_FILE_MANAGER, got[1].Kind)
}

// --- Launch: the argv, which is the whole of what this package decides ---

func TestLaunchCommand_BinaryPassesTheDirectoryAlone(t *testing.T) {
	t.Parallel()
	cmd, exitMeaningful, err := launchCommand(&detectedExec{kind: execKindBinary, path: "/usr/bin/code"}, "/repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin/code", "/repo"}, cmd.Args)
	assert.True(t, exitMeaningful)
}

// The regression test for the reported bug: a macOS launch must go through
// `open -a`, which ACTIVATES the application. Running the binary hands the
// folder to the running instance and leaves it behind the LeapMux window,
// which reads as the menu item doing nothing.
func TestLaunchCommand_MacOSAppGoesThroughOpenSoTheAppIsRaised(t *testing.T) {
	t.Parallel()
	cmd, exitMeaningful, err := launchCommand(
		&detectedExec{kind: execKindMacOSApp, path: "/Applications/Visual Studio Code.app"}, "/repo")
	require.NoError(t, err)
	assert.Equal(t, []string{"open", "-a", "/Applications/Visual Studio Code.app", "/repo"}, cmd.Args)
	assert.NotContains(t, cmd.Args, "-n", "a new instance is not wanted, only the front-most one")
	assert.True(t, exitMeaningful)
}

func TestLaunchCommand_RejectsAnUnknownKind(t *testing.T) {
	t.Parallel()
	_, _, err := launchCommand(&detectedExec{kind: execKind(99)}, "/repo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown exec kind")
}

// --- Launch: reporting a process that starts and then refuses ---

func TestStartAndWatch_ReportsAnImmediateFailureWithItsStderr(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(shellForTest(t), "-c", "echo 'no such application' >&2; exit 3")

	err := startAndWatch(cmd, true)

	require.Error(t, err, "a launcher that exits at once must not be reported as a successful open")
	assert.Contains(t, err.Error(), "no such application")
}

func TestStartAndWatch_AcceptsAProcessThatKeepsRunning(t *testing.T) {
	t.Parallel()
	// An editor holds its process for the life of the window, which is what a
	// real launch looks like: nothing to report before the deadline.
	cmd := exec.Command(shellForTest(t), "-c", "sleep 30")

	require.NoError(t, startAndWatch(cmd, true))
	assert.NoError(t, cmd.Process.Kill())
}

func TestStartAndWatch_ReportsAFailureToStartAtAll(t *testing.T) {
	t.Parallel()
	err := startAndWatch(exec.Command(filepath.Join(t.TempDir(), "definitely-not-a-program")), true)
	require.Error(t, err)
}

// Windows Explorer exits 1 after a SUCCESSFUL open, so a launcher whose exit
// code says nothing must not have that code read as a failure.
func TestStartAndWatch_IgnoresTheExitCodeWhenItCarriesNoVerdict(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(shellForTest(t), "-c", "exit 1")

	assert.NoError(t, startAndWatch(cmd, false))
}

func TestFirstLine_TakesTheFirstNonEmptyLine(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "the reason", firstLine("\n  \n the reason \nusage: ...\n"))
	assert.Empty(t, firstLine("   \n\n"))
}

// shellForTest gives a POSIX shell, skipping where there is none. The launcher
// tests need a process whose exit code and stderr they control; the behaviour
// under test is the WATCHING, which is platform-neutral.
func shellForTest(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell to drive the launcher with")
	}
	return sh
}
