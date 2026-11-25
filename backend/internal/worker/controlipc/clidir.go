package controlipc

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/util/execfile"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/useronly"
)

// cliDirectoryPrefix begins the name of the directory that holds the link of
// one Worker process. Each directory lives in the run directory of the data
// directory, beside the control sockets.
const cliDirectoryPrefix = "cli-"

// CLIDirectory is the directory that a Worker puts first on the PATH of each
// process that it spawns with remote control. It holds one entry: `leapmux`
// (`leapmux.exe` on Windows), a link to the executable that serves
// `leapmux control` for this Worker. A bare `leapmux` in a tab then runs the
// CLI of the Worker that started the tab, and not a copy that the user
// installed, which can be another version.
//
// One Worker process owns one directory, and the directory has a unique name.
// A new process therefore never finds the link of an ended one in its own
// directory, and it never has to replace a link that a running process still
// executes. Windows refuses to replace an executable that a process runs.
type CLIDirectory struct {
	dir        string
	executable string

	closeOnce sync.Once
	closeErr  error
}

// PrepareCLIDirectory creates the directory of this Worker process in the run
// directory of dataDir, with access for this account only, and links
// executable into it. It first removes each directory that an ended process
// left there: the Worker's lease on dataDir means that no other process uses
// them. A directory that it cannot remove, such as a copy that a process
// still runs on Windows, stays until a later start.
//
// executable is the program that serves `leapmux control` for this Worker. The
// link stands for the file that executable resolves to through symbolic links.
// See linkMethods for the kinds of link.
func PrepareCLIDirectory(dataDir, executable string) (*CLIDirectory, error) {
	target, err := resolveExecutable(executable)
	if err != nil {
		return nil, err
	}
	canonical, err := lifecycle.CanonicalDir(dataDir)
	if err != nil {
		return nil, err
	}
	runDir := filepath.Join(canonical, runDirName)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create the run directory %s: %w", runDir, err)
	}
	if err := removeCLIDirectories(runDir); err != nil {
		slog.Warn("a control CLI directory of an ended Worker process remains", "error", err)
	}
	dir, err := useronly.MkdirTemp(runDir, cliDirectoryPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("create a control CLI directory in %s: %w", runDir, err)
	}
	link := filepath.Join(dir, cliExecutableName(runtime.GOOS))
	method, err := linkExecutable(target, link, linkMethods)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("link the control CLI %s into %s: %w", target, dir, err), os.RemoveAll(dir))
	}
	// Also link the short `lmx` alias alongside `leapmux` so spawned tabs and
	// agents can run either name without extra PATH setup.
	aliasLink := filepath.Join(dir, cliAliasExecutableName(runtime.GOOS))
	if _, aliasErr := linkExecutable(target, aliasLink, linkMethods); aliasErr != nil {
		slog.Warn("could not link short CLI alias in control CLI directory", "alias", aliasLink, "error", aliasErr)
	}
	slog.Info("control CLI linked for spawned processes", "link", link, "target", target, "method", method)
	if !controlenv.PathEntryAllowed(dir) {
		slog.Warn("the control CLI directory holds the PATH separator, so spawned processes find the CLI only through "+controlenv.CLIVariable, "directory", dir)
	}
	return &CLIDirectory{dir: dir, executable: link}, nil
}

// Executable returns the absolute path of the link, which is the value of
// LEAPMUX_CONTROL_CLI. It returns "" for a nil directory, which a factory
// without a CLI holds.
func (d *CLIDirectory) Executable() string {
	if d == nil {
		return ""
	}
	return d.executable
}

// Close removes the directory and the link. The link only points at the
// executable, so the executable stays. Close is idempotent and nil-safe.
func (d *CLIDirectory) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if err := os.RemoveAll(d.dir); err != nil {
			d.closeErr = fmt.Errorf("remove the control CLI directory %s: %w", d.dir, err)
		}
	})
	return d.closeErr
}

// resolveExecutable returns the regular file that executable resolves to.
func resolveExecutable(executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		return "", fmt.Errorf("the control CLI executable %q is not an absolute path", executable)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve the control CLI executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect the control CLI executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("the control CLI executable %s is not a regular file", resolved)
	}
	// Windows has no execute permission bit; it runs a file by its extension.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("the control CLI executable %s is not executable", resolved)
	}
	return resolved, nil
}

// removeCLIDirectories removes every CLI directory in runDir. It continues past
// a directory that it cannot remove, and reports each failure.
func removeCLIDirectories(runDir string) error {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), cliDirectoryPrefix) {
			continue
		}
		// RemoveAll removes a symbolic link and never follows it, so an entry
		// that points elsewhere cannot make it delete what it points at.
		if err := os.RemoveAll(filepath.Join(runDir, entry.Name())); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// cliExecutableName returns the name of the link on goos. A Windows command
// search finds a program only through an extension that PATHEXT lists.
func cliExecutableName(goos string) string {
	if goos == "windows" {
		return "leapmux.exe"
	}
	return "leapmux"
}

// cliAliasExecutableName returns the short `lmx` executable alias name on goos.
func cliAliasExecutableName(goos string) string {
	if goos == "windows" {
		return "lmx.exe"
	}
	return "lmx"
}

// linkMethod creates link, which must not exist yet, as a stand-in for the
// existing file target.
type linkMethod struct {
	name   string
	create func(target, link string) error
}

// linkMethods lists the kinds of link, the preferred one first. The first one
// that succeeds wins.
//
//   - A hard link is the executable itself. The CLI therefore keeps matching
//     the running Worker even after an upgrade replaces the file at the
//     original path. It needs both paths on one file system, and Linux refuses
//     it for a file of another account under fs.protected_hardlinks.
//   - A symbolic link works across file systems. It follows the original path,
//     so an upgrade that replaces that file changes the CLI under a running
//     Worker. Windows refuses it without a privilege or Developer Mode.
//   - A copy works on every file system. It reads and writes the whole
//     executable, so it comes last.
var linkMethods = []linkMethod{
	{name: "hard link", create: os.Link},
	{name: "symbolic link", create: os.Symlink},
	{name: "copy", create: copyExecutable},
}

// linkExecutable creates link with the first method that succeeds, and
// returns the name of that method. It reports the failure of every method
// when none succeeds.
func linkExecutable(target, link string, methods []linkMethod) (string, error) {
	failures := make([]error, 0, len(methods))
	for _, method := range methods {
		err := method.create(target, link)
		if err == nil {
			return method.name, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", method.name, err))
		// A failed method can leave a partial entry. The next method needs the
		// name free.
		if removeErr := os.Remove(link); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return "", errors.Join(append(failures, removeErr)...)
		}
	}
	return "", errors.Join(failures...)
}

// copyExecutable copies target to link, which must not exist yet. The copy is
// executable by this account only, and a failure removes it.
// execfile.CreateExclusive keeps every fork of this process out while the copy
// is open for writing, so no child inherits the descriptor and no later exec
// of the copy fails with "text file busy".
func copyExecutable(target, link string) error {
	source, err := os.Open(target)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	return execfile.CreateExclusive(link, 0o700, func(w io.Writer) error {
		_, err := io.Copy(w, source)
		return err
	})
}
