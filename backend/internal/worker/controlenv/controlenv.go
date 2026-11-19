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
//     `leapmux` then runs the CLI of the Worker that started the process,
//     unless a login shell's profile moves the entry (see Apply).
//
// A spawn without remote control receives no fresh variables, so it gets no
// LEAPMUX_CONTROL_CLI and no PATH entry either. The PATH entry derives from
// the variable, so the two cannot disagree.
package controlenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/util/envutil"
)

const (
	// VariablePrefix begins the name of each variable of the remote-control
	// context.
	VariablePrefix = "LEAPMUX_CONTROL_"
	// CLIVariable holds the absolute path of the `leapmux` executable that
	// serves the Worker that started the process. The Worker owns the directory
	// of that path, and the directory holds nothing else.
	CLIVariable = VariablePrefix + "CLI"
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
	inherited := cliDirectory(environ)
	// StripByPrefix returns a new slice, so the append never writes into
	// environ.
	env := append(envutil.StripByPrefix(environ, VariablePrefix), control...)
	return style.editPath(env, inherited, cliDirectory(control))
}

// cliDirectory returns the directory of the last LEAPMUX_CONTROL_CLI value in
// env, or "" when env holds none or the value is not an absolute path. The
// name matches case-sensitively, as the prefix strip in Apply does, so the
// variable that Apply reads is the variable that it strips.
func cliDirectory(env []string) string {
	executable := ""
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == CLIVariable {
			executable = value
		}
	}
	if !filepath.IsAbs(executable) {
		return ""
	}
	return filepath.Dir(executable)
}

// pathList states how one operating system spells PATH and its entries.
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
		if (remove != "" && p.same(entry, remove)) || (prepend != "" && p.same(entry, prepend)) {
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
