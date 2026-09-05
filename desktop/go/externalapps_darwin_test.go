//go:build darwin

package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDarwinSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultExternalAppSpecs() {
		ids[spec.ID] = true
	}
	assert.True(t, ids["xcode"], "Xcode must appear on macOS")
	assert.False(t, ids["notepad-plus-plus"], "Notepad++ must not appear on macOS")
}

// Finder opens the directory's own contents. `open -R` would select it inside
// its parent instead, which is what "Reveal in file manager" does.
func TestDarwinFileManagerCommand_OpensTheDirectoryItself(t *testing.T) {
	t.Parallel()
	cmd, exitMeaningful := fileManagerCommand("/repo")
	assert.Equal(t, []string{"open", "/repo"}, cmd.Args)
	assert.NotContains(t, cmd.Args, "-R")
	assert.True(t, exitMeaningful)
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
	assert.Equal(t, execKindMacOSApp, got.kind)
	assert.Equal(t, "/Applications/Visual Studio Code.app", filepath.ToSlash(got.path))
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
	assert.Equal(t, execKindBinary, got.kind)
	assert.Equal(t, "/Users/alice/.local/bin/code", got.path)
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
	assert.Equal(t, execKindMacOSApp, got.kind)
	assert.Equal(t, "/Users/alice/Applications/JetBrains Toolbox/GoLand.app", filepath.ToSlash(got.path))
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
