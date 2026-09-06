//go:build darwin

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bundle must be probed BEFORE the PATH command on every row: only
// `open -a` activates the target on macOS, and a PATH command hands the folder
// to the running instance and exits, leaving its window behind this one. The
// rule was a comment and seven hand-written rows before `darwinSpec`, so this
// asserts the constructor delivers it for every row that uses it.
func TestDarwinSpecs_ProbeTheBundleBeforeThePathCommand(t *testing.T) {
	t.Parallel()
	for _, spec := range defaultExternalAppSpecs() {
		if spec.ID == fileManagerID {
			continue
		}
		t.Run(spec.ID, func(t *testing.T) {
			t.Parallel()
			// A machine that has BOTH: the bundle where we look, and the CLI on
			// PATH. Only the bundle-first order answers with the bundle.
			p := newFakeProber()
			p.setHome("/Users/alice")
			for _, base := range macOSAppBases {
				p.addPath(base + "/" + spec.DisplayName + ".app")
			}
			p.addLookPath("code", "/usr/local/bin/code")
			p.addLookPath("idea", "/usr/local/bin/idea")

			got := spec.detect(p)
			require.NotNil(t, got, "the bundle is present, so every row must detect it")
			assert.Equal(t, "open", argvOf(got, "/repo")[0],
				"a PATH command here would open the folder in a window that stays behind LeapMux")
		})
	}
}

// Finder opens the directory's own contents. `open -R` would select it inside
// its parent instead, which is what "Reveal in file manager" does.
func TestDarwinFileManagerCommand_OpensTheDirectoryItself(t *testing.T) {
	t.Parallel()
	plan := fileManagerCommand("/repo")
	assert.Equal(t, []string{"open", "/repo"}, plan.cmd.Args)
	assert.NotContains(t, plan.cmd.Args, "-R")
	assert.True(t, plan.exitMeaningful)
}

// The regression test for the reported bug at the table level: with both a
// bundle and a PATH command present, the bundle must win, because only
// `open -a` raises the application.
func TestDarwinVSCode_PrefersTheBundleOverThePathCommand(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addPath("/Applications/Visual Studio Code.app")
	p.addLookPath("code", "/Users/alice/.local/bin/code")

	got := findDarwinSpec(t, "vscode").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Applications/Visual Studio Code.app", got.describe)
}

// With no bundle anywhere, the PATH command is still better than nothing: the
// folder opens, even though the window may not come forward.
func TestDarwinVSCode_FallsBackToThePathCommand(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addLookPath("code", "/Users/alice/.local/bin/code")

	got := findDarwinSpec(t, "vscode").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Users/alice/.local/bin/code", got.describe)
}

// A JetBrains IDE installed only through Toolbox resolves to the Toolbox
// BUNDLE, not the Toolbox wrapper script, so the launch can raise it.
func TestDarwinJetBrains_PrefersTheToolboxBundleOverItsScript(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	p.addPath("/Users/alice/Applications/JetBrains Toolbox/GoLand.app")
	p.addPath("/Users/alice/Library/Application Support/JetBrains/Toolbox/scripts/goland")

	got := findDarwinSpec(t, "goland").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/Users/alice/Applications/JetBrains Toolbox/GoLand.app", got.describe)
}

// findDarwinSpec returns the ExternalAppSpec with the given id from the live
// macOS registry, or fails the test if no such spec exists.
func findDarwinSpec(t *testing.T, id string) ExternalAppSpec {
	t.Helper()
	for _, s := range defaultExternalAppSpecs() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("spec %q not found in defaultExternalAppSpecs()", id)
	return ExternalAppSpec{}
}

// --- macOS bundle detection and launch ---
//
// These tests assert POSIX .app paths, so they belong to the darwin build and
// cannot move to the portable test file. See the note above execMacOSApp.

func TestTryMacOSApp_ProbesBothApplicationsRoots(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("/Users/alice")
	// Only the user-Applications copy exists.
	p.addPath("/Users/alice/Applications/Visual Studio Code.app")

	got := tryMacOSApp("Visual Studio Code")(p)
	require.NotNil(t, got)
	assert.Equal(t, []string{"open", "-a", "/Users/alice/Applications/Visual Studio Code.app", "/repo"},
		argvOf(got, "/repo"), "`open -a` is the only route that RAISES the application")
	assert.Equal(t, "/Users/alice/Applications/Visual Studio Code.app", got.describe,
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
	assert.Equal(t, "/Applications/Cursor.app", got.describe)
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
	assert.Equal(t, "/Users/alice/Applications/JetBrains Toolbox/GoLand.app", got.describe)
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
	assert.Equal(t, "/Applications/Zed Preview.app", got.describe)
}

// A "~" base with no home directory would otherwise expand to a RELATIVE path
// and probe whatever the working directory holds.
func TestTryMacOSApp_SkipsRelativeCandidateWhenHomeIsEmpty(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.setHome("")
	p.addPath("Applications/Cursor.app")

	assert.Nil(t, tryMacOSApp("Cursor")(p))
}

// Only `open -a` activates the target on macOS. The bundle's own command hands
// the folder to the running instance and exits, and that leaves its window
// behind this one, so the menu item looks like it does nothing.
func TestExecMacOSApp_GoesThroughOpenSoTheAppIsRaised(t *testing.T) {
	t.Parallel()
	plan := execMacOSApp("/Applications/Visual Studio Code.app").command("/repo")

	assert.Equal(t, []string{"open", "-a", "/Applications/Visual Studio Code.app", "/repo"}, plan.cmd.Args)
	assert.True(t, plan.exitMeaningful)
}
