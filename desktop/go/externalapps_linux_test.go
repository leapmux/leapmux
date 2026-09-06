//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findLinuxSpec returns the ExternalAppSpec with the given id from the live Linux
// registry, or fails the test if no such spec exists.
func findLinuxSpec(t *testing.T, id string) ExternalAppSpec {
	t.Helper()
	for _, s := range defaultExternalAppSpecs() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("spec %q not found in defaultExternalAppSpecs()", id)
	return ExternalAppSpec{}
}

// On Arch (and NixOS), the official Zed package ships its CLI as `zeditor`
// because `zed` was already taken. Detection must find it.
func TestLinuxZed_DetectsZededitorBinary(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("zeditor", "/usr/bin/zeditor")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got, "Zed must be detected when only `zeditor` is on PATH")
	assert.Equal(t, "/usr/bin/zeditor", got.describe)
}

// On Arch, /usr/bin/zed belongs to zfs-utils (the ZFS Event Daemon), not the
// editor. If both names are on PATH we MUST pick `zeditor` to avoid invoking
// the wrong binary against the user's workspace.
func TestLinuxZed_PrefersZededitorOverZed(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("zed", "/usr/bin/zed")
	p.addLookPath("zeditor", "/usr/bin/zeditor")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/usr/bin/zeditor", got.describe,
		"zeditor is unambiguous; `zed` collides with zfs-utils and must not win")
}

// Distros without the rename (e.g. users who installed Zed via
// zed.dev/install.sh on a non-ZFS box) only have `zed` on PATH, and that
// `zed` really is the editor. Detection must still resolve.
func TestLinuxZed_FallsBackToZedWhenAlone(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("zed", "/home/u/.local/bin/zed")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/home/u/.local/bin/zed", got.describe)
}

// Flatpak install: only the dev.zed.Zed wrapper exists.
func TestLinuxZed_DetectsFlatpakWrapper(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addPath("/var/lib/flatpak/exports/bin/dev.zed.Zed")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, "/var/lib/flatpak/exports/bin/dev.zed.Zed", got.describe)
}

// xdg-open reads the desktop's own association, so LeapMux never has to name
// Nautilus, Dolphin or Thunar -- any of which may be the one installed.
func TestLinuxFileManagerCommand_DelegatesToXdgOpen(t *testing.T) {
	t.Parallel()
	plan := fileManagerCommand("/repo")
	assert.Equal(t, []string{"xdg-open", "/repo"}, plan.cmd.Args)
	assert.True(t, plan.exitMeaningful)
}
