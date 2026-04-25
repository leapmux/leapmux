//go:build linux

package main

const (
	jbToolboxScript = "~/.local/share/JetBrains/Toolbox/scripts"
	snapBin         = "/snap/bin"
	flatpakBin      = "/var/lib/flatpak/exports/bin"
)

func defaultEditorSpecs() []EditorSpec {
	return []EditorSpec{
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
			detect: tryAll(
				tryLookPath("zed"),
				tryPath(flatpakBin+"/dev.zed.Zed"),
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
func jbSpec(id, displayName, cli, snapName string) EditorSpec {
	return EditorSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryPath(jbToolboxScript+"/"+cli),
			tryLookPath(cli),
			tryPath(snapBin+"/"+snapName),
		),
	}
}
