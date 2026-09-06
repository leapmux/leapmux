//go:build darwin

package main

import (
	"path/filepath"
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
	assert.Equal(t, "/Applications/Visual Studio Code.app", filepath.ToSlash(got.describe))
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
	assert.Equal(t, "/Users/alice/Applications/JetBrains Toolbox/GoLand.app", filepath.ToSlash(got.describe))
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
