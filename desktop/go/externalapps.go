package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/leapmux/leapmux/util/validate"
)

// fileManagerID is the operating system's own file manager. It is the one id
// this package spells, because the file manager is the one application every
// platform is guaranteed to have and each per-OS table declares it by hand.
// Every other id is a spec-table row that the contract cross-checks.
const fileManagerID = "file-manager"

// ExternalApp is what the Tauri shell ultimately surfaces to the frontend: a
// stable id, a human-readable display name, and what the application IS.
// Detection state itself (which executable, where) is kept inside the registry.
type ExternalApp struct {
	ID          string
	DisplayName string
	// Kind lets the browser group its menu -- editors together, the file
	// manager on its own -- without testing an id literal. It comes from
	// contracts.ExternalAppKindByID, so the two languages cannot disagree.
	Kind desktoppb.ExternalAppKind
}

// detectedExec records HOW we plan to launch a particular application,
// captured when the registry first runs detection. There is no per-launch
// redetect.
type detectedExec struct {
	kind execKind
	// path semantics depend on kind:
	//   execKindBinary      → absolute path to the binary or shim to invoke.
	//   execKindMacOSApp    → absolute path to the .app bundle, for `open -a`.
	//   execKindFileManager → unused; the per-OS fileManagerCommand supplies
	//                         the whole command.
	path string
}

type execKind int

const (
	execKindBinary execKind = iota
	execKindMacOSApp
	execKindFileManager
)

// Prober abstracts the few filesystem / environment lookups detection needs,
// so the registry can be unit-tested without touching the real machine.
type Prober interface {
	Stat(path string) (os.FileInfo, error)
	LookPath(name string) (string, error)
	Glob(pattern string) ([]string, error)
	Home() string
	Env(name string) string
}

// Launcher actually starts the application process. Split out so tests can
// assert on what would have been launched without spawning real subprocesses.
type Launcher interface {
	Launch(detected *detectedExec, path string) error
}

// ExternalAppSpec is one row of the per-OS registry table. detect is
// responsible for both probing AND building the launch descriptor -- keeping
// that logic next to the spec keeps the table readable.
type ExternalAppSpec struct {
	ID          string
	DisplayName string
	detect      func(Prober) *detectedExec
}

// ExternalAppRegistry holds the per-OS specs plus cached detection results.
// Construct with newExternalAppRegistry; the live application uses
// defaultExternalAppRegistry.
//
// Detection is cached behind a mutex (rather than `sync.Once`) so the
// frontend's "Refresh app list" action can invalidate the cache without
// restarting the sidecar.
type ExternalAppRegistry struct {
	specs    []ExternalAppSpec
	prober   Prober
	launcher Launcher

	mu       sync.Mutex
	cached   bool
	cache    []ExternalApp
	detected map[string]*detectedExec
}

func newExternalAppRegistry(specs []ExternalAppSpec, prober Prober, launcher Launcher) *ExternalAppRegistry {
	return &ExternalAppRegistry{
		specs:    specs,
		prober:   prober,
		launcher: launcher,
	}
}

// defaultExternalAppRegistry returns the registry the desktop App uses at
// runtime, wired with the OS-native prober and launcher and the per-OS spec
// table.
func defaultExternalAppRegistry() *ExternalAppRegistry {
	return newExternalAppRegistry(defaultExternalAppSpecs(), osProber{}, osLauncher{})
}

// List runs detection (once) and returns the applications that were found, in
// the order they appear in the spec table. Safe for concurrent callers.
func (r *ExternalAppRegistry) List() []ExternalApp {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cached {
		r.detectLocked()
	}
	return r.cache
}

// Refresh forces a re-probe and returns the freshly detected applications.
// Used when the user clicks "Refresh app list" after installing or
// uninstalling an editor.
func (r *ExternalAppRegistry) Refresh() []ExternalApp {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detectLocked()
	return r.cache
}

// Open launches the named application at the given absolute directory path.
// The path is validated server-side: a misbehaving frontend cannot trick us
// into launching an application against a relative path, a missing path, or a
// file.
func (r *ExternalAppRegistry) Open(id, path string) error {
	cleaned, err := validateOpenPath(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if !r.cached {
		r.detectLocked()
	}
	detected, ok := r.detected[id]
	r.mu.Unlock()
	if !ok || detected == nil {
		return fmt.Errorf("application %q is not available", id)
	}
	if err := r.launcher.Launch(detected, cleaned); err != nil {
		return fmt.Errorf("launch %s: %w", r.displayName(id), err)
	}
	return nil
}

func (r *ExternalAppRegistry) displayName(id string) string {
	for i := range r.specs {
		if r.specs[i].ID == id {
			return r.specs[i].DisplayName
		}
	}
	return id
}

// detectLocked runs the spec table against the configured Prober and rewrites
// the cache. The caller must hold r.mu.
func (r *ExternalAppRegistry) detectLocked() {
	r.detected = make(map[string]*detectedExec, len(r.specs))
	out := make([]ExternalApp, 0, len(r.specs))
	for i := range r.specs {
		spec := r.specs[i]
		if spec.detect == nil {
			continue
		}
		if d := spec.detect(r.prober); d != nil {
			r.detected[spec.ID] = d
			out = append(out, ExternalApp{
				ID:          spec.ID,
				DisplayName: spec.DisplayName,
				Kind:        contracts.ExternalAppKindByID[spec.ID],
			})
		}
	}
	r.cache = out
	r.cached = true
}

// validateOpenPath delegates the path-shape checks (non-empty, absolute,
// no traversal, no Windows reserved names) to the shared validate package,
// then layers the launcher-specific requirement that the target actually
// exists on disk and is a directory.
func validateOpenPath(p string) (string, error) {
	homeDir, _ := os.UserHomeDir()
	cleaned, err := validate.SanitizePath(p, homeDir)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", fmt.Errorf("path not accessible: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %q", cleaned)
	}
	return cleaned, nil
}

// --- Detection helpers ---

// tryAll composes detectors: returns the first non-nil result.
func tryAll(detectors ...func(Prober) *detectedExec) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		for _, d := range detectors {
			if e := d(p); e != nil {
				return e
			}
		}
		return nil
	}
}

// tryLookPath finds a binary on PATH.
func tryLookPath(name string) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		if path, err := p.LookPath(name); err == nil {
			return &detectedExec{kind: execKindBinary, path: path}
		}
		return nil
	}
}

// tryPath probes a single absolute (or ~ / env-prefixed) path and, if it
// exists, treats it as a launchable binary.
func tryPath(raw string) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		expanded := expandPath(p, raw)
		if expanded == "" {
			return nil
		}
		if _, err := p.Stat(expanded); err == nil {
			return &detectedExec{kind: execKindBinary, path: expanded}
		}
		return nil
	}
}

// tryGlob matches a glob pattern (with ~ / env expansion) and returns the
// last match — for JetBrains versioned install dirs this means the highest
// version sorts last alphabetically (e.g. "IntelliJ IDEA Ultimate 2024.2"
// after "2023.3"), which is what users expect.
func tryGlob(pattern string) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		expanded := expandPath(p, pattern)
		if expanded == "" {
			return nil
		}
		matches, _ := p.Glob(expanded)
		if len(matches) == 0 {
			return nil
		}
		return &detectedExec{kind: execKindBinary, path: matches[len(matches)-1]}
	}
}

// macOSAppBases are the directories tryMacOSApp probes, in order.
//
// The JetBrains Toolbox directory is one of them because Toolbox installs its
// IDEs one level below ~/Applications. Without it a Toolbox user with no copy
// in /Applications falls through to the Toolbox wrapper script, and a script
// starts the IDE without ever bringing it to the front — see osLauncher.
var macOSAppBases = []string{
	"/Applications",
	"~/Applications",
	"~/Applications/JetBrains Toolbox",
}

// tryMacOSApp probes the standard application directories for any of the given
// .app bundle names. On hit, the launch descriptor carries the RESOLVED bundle
// path, so `open -a` addresses one exact copy: a bare name goes through
// LaunchServices, which is free to pick a different install of the same app.
//
// Several names because one product ships under more than one bundle name --
// "Zed" and "Zed Preview", or JetBrains' "IntelliJ IDEA" from the website
// against Toolbox's "IntelliJ IDEA Ultimate". Every base is tried for every
// name before the next base, so a direct install wins over a Toolbox copy.
func tryMacOSApp(bundleNames ...string) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		for _, base := range macOSAppBases {
			for _, name := range bundleNames {
				full := expandPath(p, filepath.Join(base, name+".app"))
				// A "~" base with no home directory expands to a RELATIVE
				// path, which would probe whatever the working directory
				// happens to be. Absolute or not at all.
				if full == "" || !filepath.IsAbs(full) {
					continue
				}
				if _, err := p.Stat(full); err == nil {
					return &detectedExec{kind: execKindMacOSApp, path: full}
				}
			}
		}
		return nil
	}
}

// fileManagerSpec builds the row for the operating system's own file manager.
//
// It never probes: every desktop has a file manager, and the app menu depends
// on that, because it renders the file manager as its own always-present
// group ahead of the editors. The per-OS fileManagerCommand supplies the
// argv.
func fileManagerSpec(displayName string) ExternalAppSpec {
	return ExternalAppSpec{
		ID:          fileManagerID,
		DisplayName: displayName,
		detect: func(Prober) *detectedExec {
			return &detectedExec{kind: execKindFileManager}
		},
	}
}

// expandPath resolves "~", "$VAR", "${VAR}", and "%VAR%" against the prober's
// view of the environment. Returns "" if a referenced variable is empty —
// callers treat that as "candidate not applicable".
func expandPath(p Prober, raw string) string {
	out := raw
	switch {
	case out == "~":
		out = p.Home()
	case strings.HasPrefix(out, "~/") || strings.HasPrefix(out, `~\`):
		out = filepath.Join(p.Home(), out[2:])
	}

	out = os.Expand(out, p.Env)
	out = winEnvPattern.ReplaceAllStringFunc(out, func(match string) string {
		name := match[1 : len(match)-1]
		v := p.Env(name)
		if v == "" {
			// Mark as unresolved by returning the original token so the
			// downstream Stat fails predictably.
			return match
		}
		return v
	})

	// If any %VAR% remained unresolved, treat the whole path as unusable.
	if winEnvPattern.MatchString(out) {
		return ""
	}
	return out
}

// %FOO% (Windows-style env reference). Allows letters, digits, underscore.
var winEnvPattern = regexp.MustCompile(`%[A-Za-z_][A-Za-z0-9_]*%`)

// --- Default Prober and Launcher (real OS) ---

type osProber struct{}

func (osProber) Stat(p string) (os.FileInfo, error) { return os.Stat(p) }
func (osProber) LookPath(n string) (string, error)  { return exec.LookPath(n) }
func (osProber) Glob(pat string) ([]string, error)  { return filepath.Glob(pat) }
func (osProber) Home() string {
	h, _ := os.UserHomeDir()
	return h
}
func (osProber) Env(name string) string { return os.Getenv(name) }

// launchFailureWindow is how long a launch waits to see whether the process it
// started fails at once.
//
// The window exists because `cmd.Start` reports a fork/exec failure and
// nothing else. A command that starts and then refuses -- a wrapper script
// whose editor is gone, `open -a` against a deleted bundle, `xdg-open` with no
// handler -- exits within milliseconds. Without this the sidecar answered OK
// and the user saw nothing happen at all, which is indistinguishable from the
// application opening behind the window.
//
// A process still alive at the deadline counts as launched, which is the
// normal case: a first launch keeps the process for the life of the editor.
const launchFailureWindow = 400 * time.Millisecond

type osLauncher struct{}

// launchCommand builds the command that opens path in the detected
// application, and says whether the command's exit code carries a verdict.
//
// Split from Launch so a test can assert the argv of every kind without
// spawning anything -- argv is the whole of what this package decides, and
// the macOS branch is the difference between raising the editor and leaving
// it behind the window.
func launchCommand(detected *detectedExec, path string) (*exec.Cmd, bool, error) {
	switch detected.kind {
	case execKindBinary:
		return exec.Command(detected.path, path), true, nil
	case execKindMacOSApp:
		// `open -a <bundle> <dir>` asks the running instance to open the
		// folder AND activates it, which is the whole reason the bundle is
		// probed before the PATH command. Running the command directly starts
		// a second process that forwards its argument to the first instance
		// and exits, leaving that instance wherever it was -- usually behind
		// this window, which reads as the click doing nothing.
		//
		// No `-n`: a new instance is not wanted, only the front-most one.
		return exec.Command("open", "-a", detected.path, path), true, nil
	case execKindFileManager:
		cmd, exitMeaningful := fileManagerCommand(path)
		return cmd, exitMeaningful, nil
	default:
		return nil, false, fmt.Errorf("unknown exec kind: %d", detected.kind)
	}
}

func (osLauncher) Launch(detected *detectedExec, path string) error {
	cmd, exitMeaningful, err := launchCommand(detected, path)
	if err != nil {
		return err
	}
	procutil.HideConsoleWindow(cmd)
	return startAndWatch(cmd, exitMeaningful)
}

// startAndWatch starts cmd and reports an immediate failure.
//
// It always reaps the child, in the goroutine below, so a session's launches
// cannot accumulate zombies. When exitMeaningful is false the exit code is
// collected and discarded: some launchers report a nonzero status for a
// perfectly good open, and calling those failures would be worse than staying
// silent.
func startAndWatch(cmd *exec.Cmd, exitMeaningful bool) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// Buffered, so the goroutine finishes and releases the child even after
	// the deadline branch below returns and nobody reads the channel.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if !exitMeaningful {
		return nil
	}
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		// cmd.Wait finished, so the stderr copier finished with it and the
		// buffer is complete and no longer written.
		if msg := firstLine(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	case <-time.After(launchFailureWindow):
		// Still running, which is what a real launch looks like.
		return nil
	}
}

// firstLine is the first non-empty line of s, for an error a person reads in a
// notification. A failing launcher can print a whole usage screen, and the
// first line is the part that says what went wrong.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
