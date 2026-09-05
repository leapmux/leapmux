//go:build darwin

package main

import "os/exec"

// jbToolboxScript is the macOS Toolbox default scripts directory.
const jbToolboxScript = "~/Library/Application Support/JetBrains/Toolbox/scripts"

// Every editor here probes its .app bundle BEFORE the PATH command, which is
// the opposite of the other two platforms. Only `open -a` activates the target
// application on macOS. A PATH command starts a second process that hands its
// argument to the running instance and exits, so the folder opens in a window
// that stays behind this one, and the user sees the menu item do nothing.
//
// The cost of the order is that a custom PATH wrapper -- one that adds
// `--reuse-window`, say -- no longer applies once the bundle is installed
// where we look. Raising the window the user asked for is worth more than
// honouring flags they cannot see.
func defaultExternalAppSpecs() []ExternalAppSpec {
	return []ExternalAppSpec{
		// The file manager leads the list, and the menu keeps it in its own
		// group ahead of the editors.
		fileManagerSpec("Finder"),

		// VS Code family
		{
			ID:          "vscode",
			DisplayName: "Visual Studio Code",
			detect: tryAll(
				tryMacOSApp("Visual Studio Code"),
				tryLookPath("code"),
			),
		},
		{
			ID:          "vscode-insiders",
			DisplayName: "Visual Studio Code - Insiders",
			detect: tryAll(
				tryMacOSApp("Visual Studio Code - Insiders"),
				tryLookPath("code-insiders"),
			),
		},
		{
			ID:          "vscodium",
			DisplayName: "VSCodium",
			detect: tryAll(
				tryMacOSApp("VSCodium"),
				tryLookPath("codium"),
			),
		},
		{
			ID:          "cursor",
			DisplayName: "Cursor",
			detect: tryAll(
				tryMacOSApp("Cursor"),
				tryLookPath("cursor"),
			),
		},
		{
			ID:          "windsurf",
			DisplayName: "Windsurf",
			detect: tryAll(
				tryMacOSApp("Windsurf"),
				tryLookPath("windsurf"),
			),
		},

		// Standalone
		{
			ID:          "sublime-text",
			DisplayName: "Sublime Text",
			detect: tryAll(
				tryMacOSApp("Sublime Text"),
				tryLookPath("subl"),
			),
		},
		{
			ID:          "zed",
			DisplayName: "Zed",
			detect: tryAll(
				tryMacOSApp("Zed", "Zed Preview"),
				tryLookPath("zed"),
			),
		},

		// JetBrains: prefer the bundle (which `open` can raise) → Toolbox
		// script (which handles updates) → PATH.
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
// .app bundle → Toolbox script → PATH.
//
// Two bundle names, because the same IDE carries different ones depending on
// where it came from: the download from the website installs "IntelliJ
// IDEA.app", while Toolbox names its copy after the product edition,
// "IntelliJ IDEA Ultimate.app" — which is the display name.
func jbSpec(id, displayName, cli, bundle string) ExternalAppSpec {
	return ExternalAppSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryMacOSApp(bundle, displayName),
			tryPath(jbToolboxScript+"/"+cli),
			tryLookPath(cli),
		),
	}
}

// fileManagerCommand opens a directory in Finder. `open <dir>` shows the
// directory's own contents and activates Finder; `open -R` would instead
// select the directory inside its parent, which is what "Reveal in file
// manager" does through the Tauri opener plugin.
//
// The exit code is meaningful: `open` reports a missing directory.
func fileManagerCommand(dir string) (*exec.Cmd, bool) {
	return exec.Command("open", dir), true
}
