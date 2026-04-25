package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/util/procutil"
)

// DetectedEditor is what the Tauri shell ultimately surfaces to the frontend:
// a stable id and a human-readable display name. Detection state itself
// (which executable, where) is kept inside the registry.
type DetectedEditor struct {
	ID          string
	DisplayName string
}

// detectedExec records HOW we plan to launch a particular editor, captured
// when the registry first runs detection. There is no per-launch redetect.
type detectedExec struct {
	kind execKind
	// path semantics depend on kind:
	//   execKindBinary   → absolute path to the binary or shim to invoke.
	//   execKindMacOSApp → bundle name (e.g. "Visual Studio Code") to pass
	//                      to `open -a`, never an absolute .app path.
	path string
}

type execKind int

const (
	execKindBinary execKind = iota
	execKindMacOSApp
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

// Launcher actually starts the editor process. Split out so tests can assert
// on what would have been launched without spawning real subprocesses.
type Launcher interface {
	Launch(detected *detectedExec, path string) error
}

// EditorSpec is one row of the per-OS registry table. detect is responsible
// for both probing AND building the launch descriptor — keeping that logic
// next to the spec keeps the table readable.
type EditorSpec struct {
	ID          string
	DisplayName string
	detect      func(Prober) *detectedExec
}

// EditorRegistry holds the per-OS specs plus cached detection results.
// Construct with newEditorRegistry; the live application uses defaultEditorRegistry.
//
// Detection is cached behind a mutex (rather than `sync.Once`) so the
// frontend's "Refresh editor list" action can invalidate the cache without
// restarting the sidecar.
type EditorRegistry struct {
	specs    []EditorSpec
	prober   Prober
	launcher Launcher

	mu       sync.Mutex
	cached   bool
	cache    []DetectedEditor
	detected map[string]*detectedExec
}

func newEditorRegistry(specs []EditorSpec, prober Prober, launcher Launcher) *EditorRegistry {
	return &EditorRegistry{
		specs:    specs,
		prober:   prober,
		launcher: launcher,
	}
}

// defaultEditorRegistry returns the registry the desktop App uses at runtime,
// wired with the OS-native prober and launcher and the per-OS spec table.
func defaultEditorRegistry() *EditorRegistry {
	return newEditorRegistry(defaultEditorSpecs(), osProber{}, osLauncher{})
}

// List runs detection (once) and returns the editors that were found, in the
// order they appear in the spec table. Safe for concurrent callers.
func (r *EditorRegistry) List() []DetectedEditor {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cached {
		r.detectLocked()
	}
	return r.cache
}

// Refresh forces a re-probe and returns the freshly detected editors. Used
// when the user clicks "Refresh editor list" after installing or uninstalling
// an editor.
func (r *EditorRegistry) Refresh() []DetectedEditor {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.detectLocked()
	return r.cache
}

// Open launches the named editor at the given absolute directory path. The
// path is validated server-side: a misbehaving frontend cannot trick us into
// launching an editor against a relative path, a missing path, or a file.
func (r *EditorRegistry) Open(id, path string) error {
	if err := validateOpenPath(path); err != nil {
		return err
	}
	r.mu.Lock()
	if !r.cached {
		r.detectLocked()
	}
	detected, ok := r.detected[id]
	r.mu.Unlock()
	if !ok || detected == nil {
		return fmt.Errorf("editor %q is not available", id)
	}
	if err := r.launcher.Launch(detected, path); err != nil {
		return fmt.Errorf("launch %s: %w", r.displayName(id), err)
	}
	return nil
}

func (r *EditorRegistry) displayName(id string) string {
	for i := range r.specs {
		if r.specs[i].ID == id {
			return r.specs[i].DisplayName
		}
	}
	return id
}

// detectLocked runs the spec table against the configured Prober and rewrites
// the cache. The caller must hold r.mu.
func (r *EditorRegistry) detectLocked() {
	r.detected = make(map[string]*detectedExec, len(r.specs))
	out := make([]DetectedEditor, 0, len(r.specs))
	for i := range r.specs {
		spec := r.specs[i]
		if spec.detect == nil {
			continue
		}
		if d := spec.detect(r.prober); d != nil {
			r.detected[spec.ID] = d
			out = append(out, DetectedEditor{ID: spec.ID, DisplayName: spec.DisplayName})
		}
	}
	r.cache = out
	r.cached = true
}

func validateOpenPath(p string) error {
	if p == "" {
		return errors.New("path is empty")
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("path must be absolute: %q", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %q", p)
	}
	return nil
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

// tryMacOSApp probes the standard /Applications and ~/Applications locations
// for an editor's .app bundle. On hit, the launch descriptor carries the
// bundle name (not the path) so the launcher can hand it to `open -a`.
func tryMacOSApp(bundleName string) func(Prober) *detectedExec {
	return func(p Prober) *detectedExec {
		bases := []string{"/Applications", "~/Applications"}
		for _, base := range bases {
			full := expandPath(p, filepath.Join(base, bundleName+".app"))
			if full == "" {
				continue
			}
			if _, err := p.Stat(full); err == nil {
				return &detectedExec{kind: execKindMacOSApp, path: bundleName}
			}
		}
		return nil
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

type osLauncher struct{}

func (osLauncher) Launch(detected *detectedExec, path string) error {
	var cmd *exec.Cmd
	switch detected.kind {
	case execKindBinary:
		cmd = exec.Command(detected.path, path)
	case execKindMacOSApp:
		// `open -a "Bundle" path` activates the running instance and asks
		// it to open the folder, which is the same window-reuse semantics
		// users get when they double-click a folder in Finder. We intentionally
		// do NOT pass `-n` (new instance).
		cmd = exec.Command("open", "-a", detected.path, path)
	default:
		return fmt.Errorf("unknown exec kind: %d", detected.kind)
	}
	procutil.HideConsoleWindow(cmd)
	return cmd.Start()
}
