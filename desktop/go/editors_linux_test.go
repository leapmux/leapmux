//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinuxSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultEditorSpecs() {
		ids[spec.ID] = true
	}
	assert.False(t, ids["xcode"], "Xcode must not appear on Linux")
	assert.False(t, ids["notepad-plus-plus"], "Notepad++ must not appear on Linux")
}

// findLinuxSpec returns the EditorSpec with the given id from the live Linux
// registry, or fails the test if no such spec exists.
func findLinuxSpec(t *testing.T, id string) EditorSpec {
	t.Helper()
	for _, s := range defaultEditorSpecs() {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("spec %q not found in defaultEditorSpecs()", id)
	return EditorSpec{}
}

// On Arch (and NixOS), the official Zed package ships its CLI as `zeditor`
// because `zed` was already taken. Detection must find it.
func TestLinuxZed_DetectsZededitorBinary(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addLookPath("zeditor", "/usr/bin/zeditor")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got, "Zed must be detected when only `zeditor` is on PATH")
	assert.Equal(t, execKindBinary, got.kind)
	assert.Equal(t, "/usr/bin/zeditor", got.path)
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
	assert.Equal(t, "/usr/bin/zeditor", got.path,
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
	assert.Equal(t, "/home/u/.local/bin/zed", got.path)
}

// Flatpak install: only the dev.zed.Zed wrapper exists.
func TestLinuxZed_DetectsFlatpakWrapper(t *testing.T) {
	t.Parallel()
	p := newFakeProber()
	p.addPath("/var/lib/flatpak/exports/bin/dev.zed.Zed")

	got := findLinuxSpec(t, "zed").detect(p)
	require.NotNil(t, got)
	assert.Equal(t, execKindBinary, got.kind)
	assert.Equal(t, "/var/lib/flatpak/exports/bin/dev.zed.Zed", got.path)
}
