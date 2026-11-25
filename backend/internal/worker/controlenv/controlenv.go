// Package controlenv applies the remote-control context of one spawn to the
// environment of the process that the Worker starts: an agent, or the shell of
// a terminal.
//
// The context is the set of LEAPMUX_CONTROL_* variables that the control IPC
// factory mints for the spawn. Every spawn path builds its environment through
// Apply, so these rules hold for each of them:
//
//   - No inherited LEAPMUX_CONTROL_* variable passes. A Worker that runs inside
//     a LeapMux tab inherits the context of that tab, and a process that the
//     Worker starts must act as itself, not as the tab of the parent Worker.
//   - The directory that an inherited LEAPMUX_CONTROL_CLI put on PATH leaves
//     PATH with that variable.
//   - The fresh variables come last.
//   - The directory of a fresh LEAPMUX_CONTROL_CLI comes first on PATH. A bare
//     `leapmux` then runs the CLI of the Worker that started the process.
//
// A spawn without remote control receives no fresh variables, so it gets no
// LEAPMUX_CONTROL_CLI and no PATH entry either. The PATH entry derives from
// the variable, so the two cannot disagree.
//
// A login shell runs its profile after it receives this environment, and the
// profile can move the PATH entry behind another `leapmux`, or drop it (see
// Apply). Two mechanisms keep `leapmux control` on the Worker's own CLI then:
//
//   - The shell command that starts an agent puts the directory first on PATH
//     again after the profile ran (see launch.Wrap). The agent, and each
//     command that it runs without a login shell of its own, find the
//     Worker's CLI first.
//   - Inside a tab, the `leapmux` CLI hands a `leapmux control` command to
//     LEAPMUX_CONTROL_CLI when that is not the running executable (see
//     controlHandoff in cmd/leapmux). This covers the shell of a terminal, and
//     a login shell that an agent starts for one command. The hand-off needs
//     a `leapmux` that has it to be first on PATH: an older release runs the
//     command itself.
package controlenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// VariablePrefix begins the name of each variable of the remote-control
	// context.
	VariablePrefix = "LEAPMUX_CONTROL_"
	// CLIVariable holds the absolute path of the `leapmux` executable that
	// serves the Worker that started the process. The Worker owns the directory
	// of that path, which holds `leapmux` and its `lmx` alias (`leapmux.exe`
	// and `lmx.exe` on Windows).
	CLIVariable = VariablePrefix + "CLI"
	// SocketVariable holds the address of the local socket of the tab, which
	// the CLI speaks to inside the tab.
	SocketVariable = VariablePrefix + "SOCK"
	// TokenVariable holds the token that the CLI presents on SocketVariable.
	TokenVariable = VariablePrefix + "TOKEN"
	// UserIDVariable holds the user that the process acts for.
	UserIDVariable = VariablePrefix + "USER_ID"
	// WorkerIDVariable holds the Worker that started the process, which is the
	// default Worker of the CLI.
	WorkerIDVariable = VariablePrefix + "WORKER_ID"
	// TabIDVariable holds the tab of the process. A Quake terminal has none.
	TabIDVariable = VariablePrefix + "TAB_ID"
	// TabTypeVariable holds the type of the tab, as its contract token
	// ("agent" or "terminal").
	TabTypeVariable = VariablePrefix + "TAB_TYPE"
	// TerminalIDVariable holds the terminal that hosts the process. It is the
	// only identifier of a Quake terminal, which has no tab.
	TerminalIDVariable = VariablePrefix + "TERMINAL_ID"
	// WorkingDirVariable holds the working directory of the tab at its start.
	WorkingDirVariable = VariablePrefix + "WORKING_DIR"
	// AgentProviderVariable holds the provider of an agent tab, as its
	// contract token.
	AgentProviderVariable = VariablePrefix + "AGENT_PROVIDER"
)

// Apply returns environ with the remote-control context of one spawn applied.
// control holds the fresh LEAPMUX_CONTROL_* variables of the spawn, or nothing
// when the spawn has no remote control. The result:
//
//   - Holds none of the LEAPMUX_CONTROL_* variables of environ.
//   - Holds no PATH entry for the directory of the LEAPMUX_CONTROL_CLI of
//     environ, unless that directory is the only entry and control gives no
//     replacement. An empty PATH would make a shell search the working
//     directory.
//   - Ends with control, in its order.
//   - Starts PATH with the directory of the LEAPMUX_CONTROL_CLI of control,
//     once.
//
// Apply never replaces PATH. It edits the PATH that the process would get
// otherwise, in place:
//
//   - An unset PATH stays unset. A PATH that held only the directory would
//     replace the default search path of the shell and of execvp.
//   - A set but empty PATH holds one empty entry, which a shell reads as the
//     working directory. The directory goes before it, and the empty entry
//     stays.
//   - A directory that holds the PATH separator cannot be a PATH entry, so
//     PATH stays as it is. LEAPMUX_CONTROL_CLI still gives the executable.
//
// A login shell runs its profile after it receives this environment, and the
// profile can move or drop the entry:
//
//   - macOS path_helper, which /etc/zprofile and /etc/profile run, moves every
//     system directory before it.
//   - Debian's /etc/profile assigns PATH in every bash and sh login shell, which
//     drops it.
//   - A user's profile that prepends a directory puts that directory first.
//
// The package comment states how an agent and the CLI recover from that.
// LEAPMUX_CONTROL_CLI gives the exact executable in every case.
//
// Apply never changes environ or control.
func Apply(environ, control []string) []string {
	return apply(environ, control, hostPathList)
}

// PathEntryAllowed reports whether dir can be an entry of PATH on this
// operating system. A directory that holds the PATH separator cannot: Apply
// leaves PATH alone for it, and only LEAPMUX_CONTROL_CLI gives the executable.
func PathEntryAllowed(dir string) bool {
	return hostPathList.entryAllowed(dir)
}

func apply(environ, control []string, style pathList) []string {
	inherited := style.cliDirectory(environ)
	// stripPrefix returns a new slice, so the append never writes into environ.
	env := append(style.stripPrefix(environ, VariablePrefix), control...)
	return style.editPath(env, inherited, style.cliDirectory(control))
}

// pathList states how one operating system spells environment variable names,
// PATH, and its entries.
type pathList struct {
	// separator joins the entries of PATH: ":" on Unix, ";" on Windows.
	separator string
	// foldCase is true on Windows, which matches variable names and paths
	// regardless of case.
	foldCase bool
}

// hostPathList is the pathList of the operating system that runs the Worker.
var hostPathList = pathList{
	separator: string(os.PathListSeparator),
	foldCase:  runtime.GOOS == "windows",
}

// same reports whether two variable names or two paths are equal on this
// operating system.
func (p pathList) same(a, b string) bool {
	if p.foldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// hasPrefix reports whether name begins with prefix under this operating
// system's variable-name matching rules.
func (p pathList) hasPrefix(name, prefix string) bool {
	if len(name) < len(prefix) {
		return false
	}
	return p.same(name[:len(prefix)], prefix)
}

// stripPrefix returns a fresh slice with every entry whose variable name starts
// with prefix removed.
func (p pathList) stripPrefix(env []string, prefix string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if p.hasPrefix(name, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// cliDirectory returns the directory of the last LEAPMUX_CONTROL_CLI value in
// env, or "" when env holds none or the value is not an absolute path. Name
// matching follows p.foldCase, matching stripPrefix.
func (p pathList) cliDirectory(env []string) string {
	executable := ""
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && p.same(name, CLIVariable) {
			executable = value
		}
	}
	if !filepath.IsAbs(executable) {
		return ""
	}
	return filepath.Dir(executable)
}

// samePathEntry compares a raw PATH entry against a clean directory path. On
// Windows it also strips surrounding double quotes and trailing path separators
// (except on root paths such as `C:\` or `\`).
func (p pathList) samePathEntry(raw, target string) bool {
	if p.same(raw, target) {
		return true
	}
	if !p.foldCase {
		return false
	}
	norm := strings.TrimSpace(raw)
	if len(norm) >= 2 && norm[0] == '"' && norm[len(norm)-1] == '"' {
		norm = norm[1 : len(norm)-1]
	}
	norm = trimWindowsTrailingSlash(norm)
	target = trimWindowsTrailingSlash(target)
	return strings.EqualFold(norm, target)
}

func trimWindowsTrailingSlash(path string) string {
	for len(path) > 1 {
		last := path[len(path)-1]
		if last != '\\' && last != '/' {
			break
		}
		if len(path) == 3 && path[1] == ':' {
			break
		}
		path = path[:len(path)-1]
	}
	return path
}

// entryAllowed reports whether dir can be an entry of PATH: it must not hold
// the separator.
func (p pathList) entryAllowed(dir string) bool {
	return !strings.Contains(dir, p.separator)
}

// editPath removes the entry remove from the effective PATH of env and puts
// prepend first, each only when it is not "". It returns env unchanged when
// env holds no PATH. See Apply for the rules.
//
// The effective PATH is the LAST entry that sets it, because exec keeps the
// last of duplicate entries. editPath writes the result at the position of
// that entry, so the order of the other variables stays, and it drops the
// earlier entries, which no process can read anyway.
func (p pathList) editPath(env []string, remove, prepend string) []string {
	if !p.entryAllowed(prepend) {
		prepend = ""
	}
	if remove == "" && prepend == "" {
		return env
	}
	last := -1
	for i, entry := range env {
		if name, _, _ := strings.Cut(entry, "="); p.same(name, "PATH") {
			last = i
		}
	}
	if last < 0 {
		return env
	}
	name, value, _ := strings.Cut(env[last], "=")
	var kept []string
	for _, entry := range strings.Split(value, p.separator) {
		if (remove != "" && p.samePathEntry(entry, remove)) || (prepend != "" && p.samePathEntry(entry, prepend)) {
			continue
		}
		kept = append(kept, entry)
	}
	if prepend != "" {
		kept = append([]string{prepend}, kept...)
	} else if len(kept) == 0 {
		// PATH held the removed directory alone. An empty PATH would make a
		// shell search the working directory, which the process did not do
		// before, so the stale entry stays.
		return env
	}
	edited := make([]string, 0, len(env))
	for i, entry := range env {
		if i == last {
			edited = append(edited, name+"="+strings.Join(kept, p.separator))
			continue
		}
		if entryName, _, _ := strings.Cut(entry, "="); !p.same(entryName, "PATH") {
			edited = append(edited, entry)
		}
	}
	return edited
}
