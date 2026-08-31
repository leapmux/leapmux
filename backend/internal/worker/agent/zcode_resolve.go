package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// ZCode is the only provider LeapMux launches that ships NO executable of its own.
// The desktop application carries a CommonJS script, `zcode.cjs`, and runs it with the
// installation's Electron binary in ELECTRON_RUN_AS_NODE mode. On macOS that binary is
// the UI-element Helper (the main Contents/MacOS executable still appears in the Dock).
// So the "probe a bare name in the login shell" model every other provider uses cannot
// find ZCode, and this file supplies the launchResolverFunc that replaces it (see
// factory.go).
//
// Resolution order:
//
//  1. The LEAPMUX_ZCODE_SCRIPT / LEAPMUX_ZCODE_NODE overrides, for an installation in a
//     place this file does not know.
//  2. A `zcode` command on PATH. A future release that ships a launcher then needs no code
//     change, and a user's own wrapper script wins over the bundle.
//  3. The bundled `zcode.cjs`, run by an interpreter that has `node:sqlite`.
//
// The interpreter requirement is not cosmetic: ZCode's app-server opens its session store
// through `node:sqlite`, which arrived in Node 22 behind a flag and is unflagged in Node
// 24. An older `node` on PATH starts and then fails inside the first RPC, which reads as a
// broken provider. So every interpreter is probed for that module before it is accepted.
const (
	// zcodeScriptEnvOverride points directly at a `zcode.cjs`.
	zcodeScriptEnvOverride = "LEAPMUX_ZCODE_SCRIPT"
	// zcodeNodeEnvOverride points at the interpreter to run it with. It is honored only
	// together with a script (an interpreter alone has nothing to run), and it REPLACES
	// interpreter discovery rather than being tried first: an operator who specifies an
	// interpreter must not silently get a different one. It is still probed for
	// `node:sqlite`, so an override that cannot work fails at launch resolution instead
	// of two RPCs later.
	zcodeNodeEnvOverride = "LEAPMUX_ZCODE_NODE"
)

// zcodeBinaryCandidates is the PATH name probed in step 2 above. It is registered with the
// factory as well, so the registry entry is complete, but NOTHING reads it there:
// ListAvailableProviders takes the launchResolver branch for ZCode, and that branch
// precedes the binaryNames one. The list is the resolver's own PATH-name constant.
var zcodeBinaryCandidates = []string{"zcode"}

// zcodeNodeSQLiteProbeScript is run by a candidate interpreter. `require` is used rather
// than `import`, because the interpreter is handed a CommonJS script (`zcode.cjs`) and must
// therefore support `require` regardless.
const zcodeNodeSQLiteProbeScript = "require('node:sqlite')"

// zcodeElectronAsNodeEnv makes ZCode's own Electron binary behave as a Node interpreter.
// Without it that binary starts the desktop application and never looks at the script
// argument.
const zcodeElectronAsNodeEnv = "ELECTRON_RUN_AS_NODE=1"

// zcodeResolveDeps holds the environment lookups launch resolution makes. Tests substitute
// them; production uses zcodeProductionDeps. The seam exists because every one of them is a
// real filesystem or a real subprocess, and the three-state contract has branches (an
// interpreter that neither works nor proves it cannot) that a live machine cannot be asked
// to produce on demand.
type zcodeResolveDeps struct {
	// getenv reads the two override variables.
	getenv func(string) string
	// fileExists reports whether a regular file is present at the path.
	fileExists func(string) bool
	// probeName answers whether a bare name resolves in the user's shell.
	probeName func(ctx context.Context, shellPath string, loginShell bool, name string) probeResult
	// resolveProgramPath returns the absolute path of a bare name in the user's shell.
	// The path is empty unless the result is probeYes.
	resolveProgramPath func(ctx context.Context, shellPath string, loginShell bool, name string) (string, probeResult)
	// probeInterpreter answers whether the program at this path can run a CommonJS
	// `require('node:sqlite')`.
	probeInterpreter func(ctx context.Context, program string, env []string) probeResult
}

func zcodeProductionDeps() zcodeResolveDeps {
	return zcodeResolveDeps{
		getenv:             os.Getenv,
		fileExists:         fileExists,
		probeName:          checkBinaryAvailable,
		resolveProgramPath: probeProgramPath,
		probeInterpreter:   probeNodeSQLite,
	}
}

// fileExists reports whether path points at an existing regular file. A directory at the path
// is NOT a file: `/opt/ZCode/resources/glm/zcode.cjs` as a directory would otherwise be
// handed to the interpreter, which fails with EISDIR at launch.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// zcodeLaunchCache memoizes launch resolution per (shell, loginShell). Resolution spawns a
// login shell and up to three interpreter subprocesses, and ListAvailableProviders plus
// every agent launch ask for the same answer. Only a CONCLUSIVE resolution is cached, for
// the same reason binaryAvailabilityCache caches only a conclusive probe: caching
// launchUnknown would freeze a load-induced timeout as a permanent verdict for the worker's
// lifetime.
var (
	zcodeLaunchCache   sync.Map // zcodeLaunchKey -> zcodeLaunchCacheEntry
	zcodeLaunchMutexes sync.Map // zcodeLaunchKey -> *sync.Mutex
)

type zcodeLaunchKey struct {
	shellPath  string
	loginShell bool
}

type zcodeLaunchCacheEntry struct {
	spec launchSpec
	res  launchResolution
}

// resolveZCodeLaunch is the registered launchResolverFunc: the cache in front of
// resolveZCodeLaunchWith.
func resolveZCodeLaunch(ctx context.Context, shellPath string, loginShell bool) (launchSpec, launchResolution) {
	key := zcodeLaunchKey{shellPath, loginShell}
	if v, ok := zcodeLaunchCache.Load(key); ok {
		e := v.(zcodeLaunchCacheEntry)
		return e.spec, e.res
	}
	// Single-flight per key, with a mutex rather than a sync.Once: an attempt that ends
	// inconclusively must be repeatable, and a spent Once cannot repeat.
	muAny, _ := zcodeLaunchMutexes.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if v, ok := zcodeLaunchCache.Load(key); ok {
		e := v.(zcodeLaunchCacheEntry)
		return e.spec, e.res
	}
	spec, res := resolveZCodeLaunchWith(ctx, shellPath, loginShell, zcodeProductionDeps())
	if res != launchUnknown {
		zcodeLaunchCache.Store(key, zcodeLaunchCacheEntry{spec, res})
	}
	return spec, res
}

// resolveZCodeLaunchWith resolves how to launch ZCode using the supplied lookups.
//
// The three-state answer is the whole point of the function, so read the returns carefully:
// launchMissing means every probe RAN and ZCode is not usable here; launchUnknown means at
// least one probe established nothing, and the caller must retry. Every return below picks
// between them deliberately.
func resolveZCodeLaunchWith(
	ctx context.Context,
	shellPath string,
	loginShell bool,
	deps zcodeResolveDeps,
) (launchSpec, launchResolution) {
	// 1. Explicit override. It replaces discovery, so a wrong value is an error the user
	// can see rather than a silent fall-through to a different installation.
	if script := strings.TrimSpace(deps.getenv(zcodeScriptEnvOverride)); script != "" {
		if !deps.fileExists(script) {
			return launchSpec{}, launchMissing
		}
		// No PATH probe ran, so nothing about it is inconclusive.
		return resolveZCodeInterpreter(ctx, shellPath, loginShell, script, deps, false)
	}

	// 2. A `zcode` launcher on PATH.
	onPath := deps.probeName(ctx, shellPath, loginShell, zcodeBinaryCandidates[0])
	if onPath == probeYes {
		return launchSpec{Program: zcodeBinaryCandidates[0]}, launchFound
	}

	// 3. The bundled script plus an interpreter that has node:sqlite.
	script, found := findZCodeScript(deps.fileExists)
	if !found {
		// No script AND no PATH launcher. That is an authoritative absence only when the
		// PATH probe actually ran: a shell that could not start says nothing about
		// whether ZCode is installed.
		if !onPath.settled() {
			return launchSpec{}, launchUnknown
		}
		return launchSpec{}, launchMissing
	}
	// The PATH probe's verdict travels WITH the interpreter search. An inconclusive probe
	// is not settled by finding a script: if no interpreter works either, the answer is
	// launchUnknown, because a working `zcode` on PATH was never ruled out -- and
	// resolveZCodeLaunch CACHES a launchMissing for the worker's life, so freezing one
	// here reports ZCode as not installed until the worker restarts.
	return resolveZCodeInterpreter(ctx, shellPath, loginShell, script, deps, !onPath.settled())
}

// zcodeInterpreter is one candidate program that might be able to run zcode.cjs.
type zcodeInterpreter struct {
	program string
	// env holds the extra KEY=VALUE entries this candidate needs -- see
	// zcodeElectronAsNodeEnv. Empty for a plain Node.
	env []string
}

// resolveZCodeInterpreter finds an interpreter for the script and builds the launch spec.
//
// Candidates are probed in preference order, and each group is resolved only when the
// previous one produced nothing, so the common case (a complete installation) spawns one
// subprocess and never touches the shell:
//
//  1. the LEAPMUX_ZCODE_NODE override, alone when it is set;
//  2. the runtime shipped INSIDE the same installation -- preferred because it is the exact
//     version ZCode itself runs the script with, so its node:sqlite behavior is the one the
//     script expects;
//  3. a `node` from the user's login shell, for an installation whose runtime was stripped.
//
// A candidate that proves nothing -- a subprocess the context killed, a shell that would
// not start -- makes the whole answer launchUnknown even when a later candidate answered
// "no" conclusively. Otherwise one timeout would be cached as "ZCode is not installed".
func resolveZCodeInterpreter(
	ctx context.Context,
	shellPath string,
	loginShell bool,
	script string,
	deps zcodeResolveDeps,
	sawInconclusive bool,
) (launchSpec, launchResolution) {
	search := zcodeInterpreterSearch{ctx: ctx, script: script, deps: deps, sawInconclusive: sawInconclusive}

	if node := strings.TrimSpace(deps.getenv(zcodeNodeEnvOverride)); node != "" {
		if spec, ok := search.try(zcodeInterpreter{program: node}); ok {
			return spec, launchFound
		}
		return launchSpec{}, search.result()
	}

	if spec, ok := search.try(bundledInterpreters(script, deps.fileExists)...); ok {
		return spec, launchFound
	}

	// The launch runs through the login shell, which sources the user's profile -- so a
	// `node` installed by nvm, mise, or fnm is on PATH there and not in the worker's own
	// environment. Resolve the name in that shell and carry the ABSOLUTE path forward, so
	// the interpreter probe below runs the same program the launch will run (and the
	// program word reaching the shell needs no PATH lookup, which is why
	// buildShellWrappedCommand quotes it).
	var fromPath []zcodeInterpreter
	for _, name := range nodeInterpreterNames() {
		path, res := deps.resolveProgramPath(ctx, shellPath, loginShell, name)
		if !res.settled() {
			search.sawInconclusive = true
			continue
		}
		if res == probeYes && path != "" {
			fromPath = append(fromPath, zcodeInterpreter{program: path})
		}
	}
	if spec, ok := search.try(fromPath...); ok {
		return spec, launchFound
	}
	return launchSpec{}, search.result()
}

// zcodeInterpreterSearch accumulates the three-state answer across candidate groups, so a
// single inconclusive probe anywhere in the search decides the whole result.
type zcodeInterpreterSearch struct {
	ctx             context.Context
	script          string
	deps            zcodeResolveDeps
	sawInconclusive bool
}

func (s *zcodeInterpreterSearch) try(candidates ...zcodeInterpreter) (launchSpec, bool) {
	for _, c := range candidates {
		res := s.deps.probeInterpreter(s.ctx, c.program, c.env)
		if res == probeYes {
			return launchSpec{
				Program:    c.program,
				PrefixArgs: []string{s.script},
				Env:        c.env,
			}, true
		}
		if !res.settled() {
			s.sawInconclusive = true
		}
	}
	return launchSpec{}, false
}

// result is the verdict once no candidate worked. An inconclusive probe anywhere makes the
// search retryable; otherwise every candidate ran and rejected the script, which is an
// established absence (the installation has no usable interpreter).
func (s *zcodeInterpreterSearch) result() launchResolution {
	if s.sawInconclusive {
		return launchUnknown
	}
	return launchMissing
}

// findZCodeScript returns the first `zcode.cjs` that exists among the per-OS install
// locations.
func findZCodeScript(exists func(string) bool) (string, bool) {
	for _, p := range zcodeScriptCandidates() {
		if exists(p) {
			return p, true
		}
	}
	return "", false
}

// zcodeScriptCandidates lists where the ZCode desktop application installs `zcode.cjs`,
// per OS and in preference order (a per-user install before a machine-wide one, because a
// user who installed their own copy means to run it).
func zcodeScriptCandidates() []string {
	const relMac = "Contents/Resources/glm/zcode.cjs"
	relOther := filepath.Join("resources", "glm", "zcode.cjs")
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		var out []string
		if home != "" {
			out = append(out, filepath.Join(home, "Applications", "ZCode.app", relMac))
		}
		return append(out, filepath.Join("/Applications", "ZCode.app", relMac))
	case "windows":
		var out []string
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			out = append(out, filepath.Join(local, "Programs", "ZCode", relOther))
		}
		for _, key := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			if base := os.Getenv(key); base != "" {
				out = append(out, filepath.Join(base, "ZCode", relOther))
			}
		}
		return out
	default:
		var out []string
		if home != "" {
			out = append(out, filepath.Join(home, ".local", "share", "ZCode", relOther))
		}
		return append(out,
			filepath.Join("/opt", "ZCode", relOther),
			filepath.Join("/usr", "share", "zcode", relOther),
			filepath.Join("/usr", "lib", "zcode", relOther),
		)
	}
}

// bundledInterpreters lists the installation's own runtime candidates that exist on disk.
func bundledInterpreters(script string, exists func(string) bool) []zcodeInterpreter {
	var out []zcodeInterpreter
	for _, p := range bundledRuntimeCandidates(script) {
		if exists(p) {
			out = append(out, zcodeInterpreter{program: p, env: []string{zcodeElectronAsNodeEnv}})
		}
	}
	return out
}

func nodeInterpreterNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"node.exe", "node"}
	}
	return []string{"node"}
}

// bundledRuntimeCandidates derives the installation's own Electron binary from the script
// path. The script always sits at <root>/<resources>/glm/zcode.cjs, so the installation
// root is three levels up, and each OS puts its executable in a known place under it.
func bundledRuntimeCandidates(script string) []string {
	root := filepath.Dir(filepath.Dir(filepath.Dir(script))) // .../Contents (macOS), else the install root
	switch runtime.GOOS {
	case "darwin":
		return bundledDarwinRuntimeCandidates(root)
	case "windows":
		return []string{filepath.Join(root, "ZCode.exe"), filepath.Join(root, "zcode.exe")}
	default:
		return []string{filepath.Join(root, "zcode"), filepath.Join(root, "ZCode")}
	}
}

// bundledDarwinRuntimeCandidates prefers Electron's UI-element Helper over the main
// application executable.
//
// The main binary lives at Contents/MacOS/<Name>. Launch Services treats that path as
// the ZCode application, so ELECTRON_RUN_AS_NODE still appears in the Dock. The
// unsuffixed Helper.app declares LSUIElement, so the same Node process does not.
// GPU/Plugin/Renderer helpers are Chromium roles and are not used as an interpreter.
func bundledDarwinRuntimeCandidates(contents string) []string {
	frameworks := filepath.Join(contents, "Frameworks")
	macOS := filepath.Join(contents, "MacOS")

	var out []string
	if entries, err := os.ReadDir(frameworks); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !isDarwinElectronHelperApp(e.Name()) {
				continue
			}
			out = append(out, darwinHelperExecutable(filepath.Join(frameworks, e.Name())))
		}
	}
	if entries, err := os.ReadDir(macOS); err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			out = append(out, filepath.Join(macOS, e.Name()))
		}
	}
	if len(out) > 0 {
		return out
	}
	return []string{
		filepath.Join(frameworks, "ZCode Helper.app", "Contents", "MacOS", "ZCode Helper"),
		filepath.Join(macOS, "ZCode"),
	}
}

// isDarwinElectronHelperApp reports whether name is Electron's unsuffixed Helper
// bundle: "ZCode Helper.app", not "ZCode Helper (GPU).app".
func isDarwinElectronHelperApp(name string) bool {
	const suffix = " Helper.app"
	if !strings.HasSuffix(name, suffix) {
		return false
	}
	stem := strings.TrimSuffix(name, ".app")
	return !strings.Contains(stem, " (")
}

// darwinHelperExecutable is the Mach-O inside a Helper.app. The executable's name
// matches the bundle stem; a missing MacOS directory still yields that conventional
// path so a stubbed exists() check can accept a Helper that is not on this machine.
func darwinHelperExecutable(helperApp string) string {
	macOS := filepath.Join(helperApp, "Contents", "MacOS")
	stem := strings.TrimSuffix(filepath.Base(helperApp), ".app")
	if entries, err := os.ReadDir(macOS); err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			return filepath.Join(macOS, e.Name())
		}
	}
	return filepath.Join(macOS, stem)
}

// probeNodeSQLite runs `<program> -e "require('node:sqlite')"` and reports whether it
// succeeded, and whether the attempt ESTABLISHED anything.
//
// The program is executed DIRECTLY, not through the user's shell. Every candidate reaching
// here is an absolute path, so no PATH lookup is needed, and avoiding the shell avoids
// nesting a quoted script inside a quoted -c argument, where one dialect's escaping rules
// would silently corrupt the probe.
//
// Exit status alone cannot separate "this interpreter has no node:sqlite" from "this
// program could not be started at all", so:
//
//   - an interpreter that ran and exited 0 -- conclusive yes;
//   - an interpreter that ran and exited non-zero (no such module, or a syntax error
//     because the program is not a JavaScript engine) -- conclusive no;
//   - a program that could not be started (absent, not executable, EACCES, a fork failure
//     under load) or whose process the context killed -- inconclusive.
func probeNodeSQLite(ctx context.Context, program string, env []string) probeResult {
	cmd := exec.CommandContext(ctx, program, "-e", zcodeNodeSQLiteProbeScript)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), env...)
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	// Both streams are discarded: a working interpreter still writes an ExperimentalWarning
	// about node:sqlite to stderr, and the exit status is the whole answer.
	err := cmd.Run()
	if ctx.Err() != nil {
		return probeUnknown
	}
	if err == nil {
		return probeYes
	}
	// An ExitError means the program ran and rejected the script. Anything else -- the
	// executable was absent, was not executable, or could not be forked -- proves nothing
	// about node:sqlite.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return probeNo
	}
	return probeUnknown
}

// probeProgramPath asks the user's shell for the ABSOLUTE path of a bare program name, and
// reports whether the answer establishes anything.
//
// It is the path-returning sibling of probeBinary and uses the same reached-markers for the
// same reason: a login profile that exits before the inner command must not be read as "the
// program is absent". ZCode needs the path (rather than probeBinary's boolean) so a resolved
// interpreter can be probed for node:sqlite directly, without a second shell.
func probeProgramPath(ctx context.Context, shellPath string, loginShell bool, name string) (string, probeResult) {
	shellName := terminal.ShellBaseName(shellPath)
	quoted := posixQuote(name)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = fmt.Sprintf(
			"$c = Get-Command %s -ErrorAction SilentlyContinue; if ($c) { Write-Output '%s'; Write-Output $c.Source } else { Write-Output '%s' }",
			pwshQuote(name), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-Command"
	case shellName == "nu":
		inner = fmt.Sprintf(
			"let p = (which %s); if ($p | is-not-empty) { echo '%s'; echo ($p | get 0.path) } else { echo '%s' }",
			nuQuote(name), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = fmt.Sprintf(
			"which %s >& /dev/null && printf '%%s\\n' '%s' && which %s || printf '%%s\\n' '%s'",
			quoted, probeReachedPresent, quoted, probeReachedAbsent,
		)
		flag = "-c"
	default:
		inner = fmt.Sprintf(
			"if p=$(command -v %s 2>/dev/null); then printf '%%s\\n%%s\\n' '%s' \"$p\"; else printf '%%s\\n' '%s'; fi",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	}

	args := terminal.CommandArgs(shellPath, loginShell, flag, inner)
	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Dir = os.TempDir()
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	if ctx.Err() != nil {
		return "", probeUnknown
	}
	return parseProgramPathProbe(stdout.String())
}

// parseProgramPathProbe extracts the path that follows the reached-present marker.
//
// A present marker with no path after it is INCONCLUSIVE, not "absent": the shell reached
// the inner command and said the program resolves, so reporting absence would contradict
// the only evidence there is. A path that is not absolute is likewise inconclusive -- a
// shell builtin or function name cannot be executed directly.
func parseProgramPathProbe(out string) (string, probeResult) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case probeReachedPresent:
			for _, rest := range lines[i+1:] {
				p := strings.TrimSpace(rest)
				if p == "" {
					continue
				}
				if !filepath.IsAbs(p) {
					return "", probeUnknown
				}
				return p, probeYes
			}
			return "", probeUnknown
		case probeReachedAbsent:
			return "", probeNo
		}
	}
	return "", probeUnknown
}
