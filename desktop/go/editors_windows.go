//go:build windows

package main

const (
	jbToolboxScript = `%LOCALAPPDATA%\JetBrains\Toolbox\scripts`
	programFiles    = `%PROGRAMFILES%`
	programFilesX86 = `%PROGRAMFILES(X86)%`
	localAppData    = `%LOCALAPPDATA%`
)

func defaultEditorSpecs() []EditorSpec {
	return []EditorSpec{
		// VS Code family
		{
			ID:          "vscode",
			DisplayName: "Visual Studio Code",
			detect: tryAll(
				tryLookPath("code"),
				tryPath(localAppData+`\Programs\Microsoft VS Code\bin\code.cmd`),
				tryPath(programFiles+`\Microsoft VS Code\bin\code.cmd`),
			),
		},
		{
			ID:          "vscode-insiders",
			DisplayName: "Visual Studio Code - Insiders",
			detect: tryAll(
				tryLookPath("code-insiders"),
				tryPath(localAppData+`\Programs\Microsoft VS Code Insiders\bin\code-insiders.cmd`),
				tryPath(programFiles+`\Microsoft VS Code Insiders\bin\code-insiders.cmd`),
			),
		},
		{
			ID:          "vscodium",
			DisplayName: "VSCodium",
			detect: tryAll(
				tryLookPath("codium"),
				tryPath(localAppData+`\Programs\VSCodium\bin\codium.cmd`),
				tryPath(programFiles+`\VSCodium\bin\codium.cmd`),
			),
		},
		{
			ID:          "cursor",
			DisplayName: "Cursor",
			detect: tryAll(
				tryLookPath("cursor"),
				tryPath(localAppData+`\Programs\Cursor\Cursor.exe`),
			),
		},
		{
			ID:          "windsurf",
			DisplayName: "Windsurf",
			detect: tryAll(
				tryLookPath("windsurf"),
				tryPath(localAppData+`\Programs\Windsurf\Windsurf.exe`),
			),
		},

		// Standalone
		{
			ID:          "sublime-text",
			DisplayName: "Sublime Text",
			detect: tryAll(
				tryLookPath("subl"),
				tryPath(programFiles+`\Sublime Text\subl.exe`),
				tryPath(programFilesX86+`\Sublime Text\subl.exe`),
			),
		},
		{
			ID:          "zed",
			DisplayName: "Zed",
			detect: tryAll(
				tryLookPath("zed"),
				tryPath(localAppData+`\Programs\Zed\Zed.exe`),
			),
		},

		// JetBrains
		jbSpec("intellij-idea-ultimate", "IntelliJ IDEA Ultimate", "idea", "IntelliJ IDEA*", "idea64.exe"),
		jbSpec("intellij-idea-community", "IntelliJ IDEA Community", "idea-ce", "IntelliJ IDEA Community Edition*", "idea64.exe"),
		jbSpec("webstorm", "WebStorm", "webstorm", "WebStorm*", "webstorm64.exe"),
		jbSpec("goland", "GoLand", "goland", "GoLand*", "goland64.exe"),
		jbSpec("rustrover", "RustRover", "rustrover", "RustRover*", "rustrover64.exe"),
		jbSpec("pycharm-professional", "PyCharm Professional", "pycharm", "PyCharm*", "pycharm64.exe"),
		jbSpec("pycharm-community", "PyCharm Community", "pycharm-ce", "PyCharm Community Edition*", "pycharm64.exe"),
		jbSpec("phpstorm", "PhpStorm", "phpstorm", "PhpStorm*", "phpstorm64.exe"),
		jbSpec("rubymine", "RubyMine", "rubymine", "RubyMine*", "rubymine64.exe"),
		jbSpec("clion", "CLion", "clion", "CLion*", "clion64.exe"),
		jbSpec("rider", "Rider", "rider", "JetBrains Rider*", "rider64.exe"),
		jbSpec("datagrip", "DataGrip", "datagrip", "DataGrip*", "datagrip64.exe"),
		jbSpec("fleet", "Fleet", "fleet", "Fleet*", "Fleet.exe"),

		// Android Studio is shipped by Google, not via JetBrains Toolbox. Direct
		// install puts it under Program Files\Android\Android Studio.
		{
			ID:          "android-studio",
			DisplayName: "Android Studio",
			detect: tryAll(
				tryPath(jbToolboxScript+`\studio.cmd`),
				tryLookPath("studio"),
				tryPath(programFiles+`\Android\Android Studio\bin\studio64.exe`),
			),
		},

		// Notepad++
		{
			ID:          "notepad-plus-plus",
			DisplayName: "Notepad++",
			detect: tryAll(
				tryPath(programFiles+`\Notepad++\notepad++.exe`),
				tryPath(programFilesX86+`\Notepad++\notepad++.exe`),
			),
		},
	}
}

// jbSpec composes the standard JetBrains detection chain on Windows:
// Toolbox script → PATH → versioned Program Files install.
func jbSpec(id, displayName, cli, productGlob, exe string) EditorSpec {
	return EditorSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryPath(jbToolboxScript+`\`+cli+`.cmd`),
			tryLookPath(cli),
			tryGlob(programFiles+`\JetBrains\`+productGlob+`\bin\`+exe),
		),
	}
}
