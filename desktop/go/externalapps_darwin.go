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
		darwinSpec("vscode", "Visual Studio Code", "code"),
		darwinSpec("vscode-insiders", "Visual Studio Code - Insiders", "code-insiders"),
		darwinSpec("vscodium", "VSCodium", "codium"),
		darwinSpec("cursor", "Cursor", "cursor"),
		darwinSpec("windsurf", "Windsurf", "windsurf"),

		// Standalone
		darwinSpec("sublime-text", "Sublime Text", "subl"),
		// Zed ships a Preview channel beside the stable one, under its own
		// bundle name.
		darwinSpec("zed", "Zed", "zed", "Zed Preview"),

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

// darwinSpec constructs the standard macOS detection chain: the .app bundle
// first, then the PATH command.
//
// A constructor rather than seven hand-written rows, because the ORDER is the
// whole fix and nothing else enforces it. The comment at the top of this file
// states the rule, and a comment is what the previous shape relied on: a new
// row written the other way round compiles, detects the editor, and reopens
// the bug this file exists to close -- with no test failing, because the
// fall-through to PATH is a supported path in its own right.
//
// The bundle name is the DISPLAY name, which is true of every row here.
// `extraBundles` covers a product that ships under more than one, which today
// is Zed's Preview channel.
func darwinSpec(id, displayName, cli string, extraBundles ...string) ExternalAppSpec {
	return ExternalAppSpec{
		ID:          id,
		DisplayName: displayName,
		detect: tryAll(
			tryMacOSApp(append([]string{displayName}, extraBundles...)...),
			tryLookPath(cli),
		),
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
func fileManagerCommand(dir string) launchPlan {
	return launchPlan{exec.Command("open", dir), true}
}
