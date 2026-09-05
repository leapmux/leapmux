//go:build linux

package main

import "os/exec"

const (
	jbToolboxScript = "~/.local/share/JetBrains/Toolbox/scripts"
	snapBin         = "/snap/bin"
	flatpakBin      = "/var/lib/flatpak/exports/bin"
)

func defaultExternalAppSpecs() []ExternalAppSpec {
	return []ExternalAppSpec{
		// The file manager leads the list, and the menu keeps it in its own
		// group ahead of the editors.
		fileManagerSpec("File Manager"),

		// VS Code family
		{
			ID:          "vscode",
			DisplayName: "Visual Studio Code",
			detect: tryAll(
				tryLookPath("code"),
				tryPath(snapBin+"/code"),
				tryPath(flatpakBin+"/com.visualstudio.code"),
			),
		},
		{
			ID:          "vscode-insiders",
			DisplayName: "Visual Studio Code - Insiders",
			detect: tryAll(
				tryLookPath("code-insiders"),
				tryPath(snapBin+"/code-insiders"),
				tryPath(flatpakBin+"/com.visualstudio.code.insiders"),
			),
		},
		{
			ID:          "vscodium",
			DisplayName: "VSCodium",
			detect: tryAll(
				tryLookPath("codium"),
				tryPath(snapBin+"/codium"),
				tryPath(flatpakBin+"/com.vscodium.codium"),
			),
		},
		{
			ID:          "cursor",
			DisplayName: "Cursor",
			detect:      tryLookPath("cursor"),
		},
		{
			ID:          "windsurf",
			DisplayName: "Windsurf",
			detect:      tryLookPath("windsurf"),
		},

		// Standalone
		{
			ID:          "sublime-text",
			DisplayName: "Sublime Text",
			detect: tryAll(
				tryLookPath("subl"),
				tryPath(snapBin+"/subl"),
				tryPath(flatpakBin+"/com.sublimetext.three"),
			),
		},
		{
			ID:          "zed",
			DisplayName: "Zed",
			// `zed` on PATH is the ZFS Event Daemon on systems with
			// zfs-utils (Arch) or zfsutils-linux (Debian/Ubuntu) — unrelated
			// to the editor. Distros that hit that collision (Arch's
			// `extra/zed`, NixOS's `zed-editor`) ship the editor's CLI as
			// `zeditor`; the Flatpak wrapper `dev.zed.Zed` is similarly
			// unambiguous. Probe those first; fall through to `zed` last.
			detect: tryAll(
				tryLookPath("zeditor"),
				tryPath(flatpakBin+"/dev.zed.Zed"),
				tryLookPath("zed"),
			),
		},

		// JetBrains
		jbSpec("intellij-idea-ultimate", "IntelliJ IDEA Ultimate", "idea", "intellij-idea-ultimate"),
		jbSpec("intellij-idea-community", "IntelliJ IDEA Community", "idea-ce", "intellij-idea-community"),
		jbSpec("webstorm", "WebStorm", "webstorm", "webstorm"),
		jbSpec("goland", "GoLand", "goland", "goland"),
		jbSpec("rustrover", "RustRover", "rustrover", "rustrover"),
		jbSpec("pycharm-professional", "PyCharm Professional", "pycharm", "pycharm-professional"),
		jbSpec("pycharm-community", "PyCharm Community", "pycharm-ce", "pycharm-community"),
		jbSpec("phpstorm", "PhpStorm", "phpstorm", "phpstorm"),
		jbSpec("rubymine", "RubyMine", "rubymine", "rubymine"),
		jbSpec("clion", "CLion", "clion", "clion"),
		jbSpec("rider", "Rider", "rider", "rider"),
		jbSpec("datagrip", "DataGrip", "datagrip", "datagrip"),
		jbSpec("android-studio", "Android Studio", "studio", "android-studio"),
		jbSpec("fleet", "Fleet", "fleet", "fleet"),
	}
}

// jbSpec composes the standard JetBrains detection chain on Linux:
// Toolbox script → PATH → Snap.
func jbSpec(id, displayName, cli, snapName string) ExternalAppSpec {
	return ExternalAppSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryPath(jbToolboxScript+"/"+cli),
			tryLookPath(cli),
			tryPath(snapBin+"/"+snapName),
		),
	}
}

// fileManagerCommand opens a directory in whichever file manager the desktop
// registered for a directory. `xdg-open` is the portable route: it reads the
// desktop's own association rather than naming Nautilus, Dolphin or Thunar,
// any of which may be the one installed.
//
// PATH order stays as it is on this platform, unlike macOS: a Linux editor
// raises its own window through the window manager when a second invocation
// hands the folder to the running instance, so there is nothing an `open`
// equivalent would fix here.
//
// The exit code is meaningful: xdg-open reports "no method available" and a
// missing file with distinct nonzero codes.
func fileManagerCommand(dir string) (*exec.Cmd, bool) {
	return exec.Command("xdg-open", dir), true
}
