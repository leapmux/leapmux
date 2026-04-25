//go:build darwin

package main

// jbToolboxScript is the macOS Toolbox default scripts directory.
const jbToolboxScript = "~/Library/Application Support/JetBrains/Toolbox/scripts"

func defaultEditorSpecs() []EditorSpec {
	return []EditorSpec{
		// VS Code family
		{
			ID:          "vscode",
			DisplayName: "Visual Studio Code",
			detect: tryAll(
				tryLookPath("code"),
				tryMacOSApp("Visual Studio Code"),
			),
		},
		{
			ID:          "vscode-insiders",
			DisplayName: "Visual Studio Code - Insiders",
			detect: tryAll(
				tryLookPath("code-insiders"),
				tryMacOSApp("Visual Studio Code - Insiders"),
			),
		},
		{
			ID:          "vscodium",
			DisplayName: "VSCodium",
			detect: tryAll(
				tryLookPath("codium"),
				tryMacOSApp("VSCodium"),
			),
		},
		{
			ID:          "cursor",
			DisplayName: "Cursor",
			detect: tryAll(
				tryLookPath("cursor"),
				tryMacOSApp("Cursor"),
			),
		},
		{
			ID:          "windsurf",
			DisplayName: "Windsurf",
			detect: tryAll(
				tryLookPath("windsurf"),
				tryMacOSApp("Windsurf"),
			),
		},

		// Standalone
		{
			ID:          "sublime-text",
			DisplayName: "Sublime Text",
			detect: tryAll(
				tryLookPath("subl"),
				tryMacOSApp("Sublime Text"),
			),
		},
		{
			ID:          "zed",
			DisplayName: "Zed",
			detect: tryAll(
				tryLookPath("zed"),
				tryMacOSApp("Zed"),
				tryMacOSApp("Zed Preview"),
			),
		},

		// JetBrains: prefer Toolbox script (handles updates) → PATH → bundle.
		jbSpec("intellij-idea-ultimate", "IntelliJ IDEA Ultimate", "idea", "IntelliJ IDEA"),
		jbSpec("intellij-idea-community", "IntelliJ IDEA Community", "idea-ce", "IntelliJ IDEA CE"),
		jbSpec("webstorm", "WebStorm", "webstorm", "WebStorm"),
		jbSpec("goland", "GoLand", "goland", "GoLand"),
		jbSpec("rustrover", "RustRover", "rustrover", "RustRover"),
		jbSpec("pycharm-professional", "PyCharm Professional", "pycharm", "PyCharm"),
		jbSpec("pycharm-community", "PyCharm Community", "pycharm-ce", "PyCharm CE"),
		jbSpec("phpstorm", "PhpStorm", "phpstorm", "PhpStorm"),
		jbSpec("rubymine", "RubyMine", "rubymine", "RubyMine"),
		jbSpec("clion", "CLion", "clion", "CLion"),
		jbSpec("rider", "Rider", "rider", "Rider"),
		jbSpec("datagrip", "DataGrip", "datagrip", "DataGrip"),
		jbSpec("android-studio", "Android Studio", "studio", "Android Studio"),
		jbSpec("fleet", "Fleet", "fleet", "Fleet"),

		// Apple
		{
			ID:          "xcode",
			DisplayName: "Xcode",
			detect:      tryMacOSApp("Xcode"),
		},
	}
}

// jbSpec constructs the standard JetBrains detection chain on macOS:
// Toolbox script → PATH → /Applications/<Bundle>.app.
func jbSpec(id, displayName, cli, bundle string) EditorSpec {
	return EditorSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryPath(jbToolboxScript+"/"+cli),
			tryLookPath(cli),
			tryMacOSApp(bundle),
		),
	}
}
