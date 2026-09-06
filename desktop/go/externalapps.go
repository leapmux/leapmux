package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/procutil"
	"github.com/leapmux/leapmux/util/validate"
)

// fileManagerID is the operating system's own file manager. It is the one id
// this package spells, because the file manager is the one application every
// platform is guaranteed to have and each per-OS table declares it by hand.
// Every other id is a spec-table row that the contract cross-checks.
const fileManagerID = "file-manager"

// ExternalApp is what the Tauri shell ultimately surfaces to the frontend: a
// stable id and a human-readable display name. Detection state itself (which
// executable, where) is kept inside the registry.
//
// No kind. What an application IS is a compile-time fact of
// contracts/external-apps.json, and the browser reads it from the table
// generated from that same file -- keyed by the id below, which a Go table test
// compares against the contract in both directions.
type ExternalApp struct {
	ID          string
	DisplayName string
}

// launchPlan is one resolved way to open a directory: the command to run, and
// whether that command's exit code carries a verdict.
//
// The flag travels WITH the command rather than beside it, because only the
// thing that built the argv knows the answer -- `explorer.exe` exits 1 after a
// successful open, so reading its code would report every launch as failed.
type launchPlan struct {
	cmd            *exec.Cmd
	exitMeaningful bool
}

// detectedExec records HOW we plan to launch a particular application,
// captured when the registry first runs detection. There is no per-launch
// redetect.
//
// The launch is a FUNCTION, not a tag this package switches on. A detector
// cannot produce a candidate without also saying how to open it, so a new way
// to launch cannot arrive without its argv -- where a tag plus a switch let one
// arrive without its case and fail at the user's click. Nothing catches that
// earlier: `.golangci.yml` sets `default-signifies-exhaustive`, so a missing
// case is not a lint error either.
type detectedExec struct {
	// What was detected, for an error and for a test to name. Never an argv:
	// `command` owns that.
	describe string
	command  func(dir string) launchPlan
}

// execBinary launches a directory by handing it to a binary or a shim.
func execBinary(path string) *detectedExec {
	return &detectedExec{
		describe: path,
		command:  func(dir string) launchPlan { return launchPlan{exec.Command(path, dir), true} },
	}
}

// execMacOSApp launches a directory through an .app bundle.
//
// `open -a <bundle> <dir>` asks the running instance to open the folder AND
// activates it, which is the whole reason the bundle is probed before the PATH
// command. Running the bundle's own command directly starts a second process
// that forwards its argument to the first instance and exits, leaving that
// instance wherever it was -- usually behind this window, which reads as the
// click doing nothing.
//
// No `-n`: a new instance is not wanted, only the front-most one.
func execMacOSApp(bundle string) *detectedExec {
	return &detectedExec{
		describe: bundle,
		command: func(dir string) launchPlan {
			return launchPlan{exec.Command("open", "-a", bundle, dir), true}
		},
	}
}

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
//
// The result is a COPY. detectLocked builds the cache with spare capacity, so
// a caller that appended to the returned slice would write into the registry's
// own backing array while another goroutine reads it.
func (r *ExternalAppRegistry) List() []ExternalApp {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cached {
		r.detectLocked()
	}
	return slices.Clone(r.cache)
}

// Refresh forces a re-probe and returns the freshly detected applications.
// Used when the user clicks "Refresh app list" after installing or uninstalling
// an application. The result is a copy, for the reason List states.
func (r *ExternalAppRegistry) Refresh() []ExternalApp {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detectLocked()
	return slices.Clone(r.cache)
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
		// The display name, not the id: the browser puts this sentence under a
		// title it already built from the display name, and two spellings of
		// one application in one notification read as two applications.
		return fmt.Errorf("%s is not available", r.displayName(id))
	}
	if err := r.launcher.Launch(detected, cleaned); err != nil {
		return fmt.Errorf("launch %s: %w", r.displayName(id), err)
	}
	return nil
}

// displayName is the human-readable name for an id, for an error a person
// reads. It takes no lock, because r.specs is written once in
// newExternalAppRegistry and never mutated afterwards.
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
			out = append(out, ExternalApp{ID: spec.ID, DisplayName: spec.DisplayName})
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
			return execBinary(path)
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
			return execBinary(expanded)
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
		matches, err := p.Glob(expanded)
		if err != nil {
			// filepath.Glob fails only on a malformed pattern, so this is a
			// spec-table bug, not a machine that lacks the application.
			// Silence would file it under "not installed", where nobody looks.
			slog.Warn("desktop sidecar: external-app glob pattern is malformed",
				"pattern", expanded, "error", err)
			return nil
		}
		if len(matches) == 0 {
			return nil
		}
		return execBinary(matches[len(matches)-1])
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
				// expandPath answers "" for a "~" base with no home
				// directory, so no candidate here can be working-directory
				// relative.
				full := expandPath(p, filepath.Join(base, name+".app"))
				if full == "" {
					continue
				}
				if _, err := p.Stat(full); err == nil {
					return execMacOSApp(full)
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
			return &detectedExec{describe: displayName, command: fileManagerCommand}
		},
	}
}

// expandPath resolves "~", "$VAR", "${VAR}", and "%VAR%" against the prober's
// view of the environment. It returns "" when any of those references does not
// resolve, and every caller treats "" as "candidate not applicable".
//
// One rule covers all three syntaxes, and it lives HERE rather than at a probe
// helper, because a half-expanded path is dangerous in two different ways and
// each helper would have to guard against both. A "~" that does not resolve
// leaves a RELATIVE path, which probes the process working directory and, on a
// hit, becomes the argv of a real launch. An unset "$VAR" is worse, because
// os.Expand substitutes the empty string silently: "$OPT/JetBrains/scripts"
// becomes "/JetBrains/scripts" and probes an absolute path nobody asked for.
// A sidecar started by launchd, by systemd, or inside a container reaches both
// conditions with an ordinary spec table.
//
// The tilde half is pathutil.ExpandHome, shared with the backend rather than
// spelled again: that helper's own doc records how this repo came to hold four
// copies of the rule, and this package would have been the fifth. It was moved
// out of `internal/` for this, because the desktop sidecar is its own module.
func expandPath(p Prober, raw string) string {
	out := raw
	if strings.HasPrefix(out, "~") {
		// pathutil.ExpandHome answers the input UNCHANGED for a home it cannot
		// resolve, and for a form it does not accept -- `~user`, and `~\` off
		// Windows. A leading "~" that survives it is therefore a RELATIVE path,
		// which would probe the process working directory and, on a hit, become
		// the argv of a real launch.
		out = pathutil.ExpandHome(out, p.Home())
		if strings.HasPrefix(out, "~") {
			return ""
		}
	}

	resolved := true
	out = os.Expand(out, func(name string) string {
		v := p.Env(name)
		if v == "" {
			resolved = false
		}
		return v
	})
	out = winEnvPattern.ReplaceAllStringFunc(out, func(match string) string {
		v := p.Env(match[1 : len(match)-1])
		if v == "" {
			resolved = false
		}
		return v
	})
	if !resolved {
		return ""
	}
	return out
}

// %FOO% (Windows-style env reference).
//
// A Windows variable name accepts every character except "%" itself, so the
// class excludes only "%" and the two path separators -- the separators
// because a name cannot span a path segment, and stopping there keeps one
// unresolved "%" from swallowing the rest of the path. An identifier-shaped
// class ([A-Za-z_][A-Za-z0-9_]*) would be wrong: %PROGRAMFILES(X86)% is a real
// variable that the Windows spec table needs, and its parentheses put it
// outside that syntax.
var winEnvPattern = regexp.MustCompile(`%[^%\\/]+%`)

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
// normal case: a first launch of an editor keeps the process for the life of
// its window.
const launchFailureWindow = 400 * time.Millisecond

// stderrCaptureLimit caps what one launch keeps of a command's stderr.
//
// Only the first line ever reaches the user. A process that survives the
// failure window keeps writing into this buffer for as long as it runs, and a
// GUI application logs for hours, so an uncapped buffer grows without limit in
// a sidecar that never restarts.
const stderrCaptureLimit = 8 << 10

type osLauncher struct{}

func (osLauncher) Launch(detected *detectedExec, path string) error {
	plan := detected.command(path)
	procutil.HideConsoleWindow(plan.cmd)
	return startAndWatch(plan.cmd, plan.exitMeaningful)
}

// startAndWatch starts cmd and reports an immediate failure.
//
// It always reaps the child, in the goroutine below, so a session's launches
// cannot accumulate zombies. That goroutine, and the stderr pipe os/exec makes
// for the capture, stay alive until every process holding the pipe's write end
// closes it -- a launcher shim exits at once but the editor it started inherits
// the pipe, so in practice that is when the user quits the editor. The capture
// is capped for exactly that reason; the goroutine and the descriptors are the
// price of reaping the child at all.
//
// cmd.WaitDelay does NOT belong here, although it looks like the bound this
// wants. It closes the parent's end of the pipe once the delay expires, so the
// editor's next write to stderr takes an EPIPE -- and a SIGPIPE with it. The
// sidecar must not risk killing the application the user just opened to
// release one file descriptor sooner.
//
// When exitMeaningful is false the exit code is collected and discarded: some
// launchers report a nonzero status for a perfectly good open, and calling
// those failures would be worse than staying silent.
func startAndWatch(cmd *exec.Cmd, exitMeaningful bool) error {
	stderr := &cappedBuffer{limit: stderrCaptureLimit}
	cmd.Stderr = stderr
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

// cappedBuffer collects at most limit bytes of a command's output and discards
// the rest. It always reports a complete write, so the process it captures
// keeps running after the cap fills instead of failing on a short write.
//
// Only startAndWatch's own goroutine writes it, and only the branch that waited
// for cmd.Wait reads it, so it needs no lock of its own.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

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
