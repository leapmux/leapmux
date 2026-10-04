package codewhale

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// Codewhale ships a native binary, and its npm package puts a Node wrapper on
// PATH in front of it. The wrapper runs the binary with spawnSync, so the
// runtime is a CHILD of a Node process, and a SIGTERM to the Node process
// orphans the runtime: a live probe found the runtime reparented to PID 1. The
// worker kills the whole process group on Stop, which reaches the child as
// well, but the wrapper also costs about 54 MB for nothing. So the locator asks
// the shell where `codewhale` is and, when that is the npm wrapper, runs the
// native binary that the wrapper would have run.
//
// The npm package keeps the binary at `<package>/bin/downloads/codewhale`
// (`scripts/artifacts.js` releaseBinaryDirectory). The wrapper on PATH reaches
// the package in one of three ways, and each has a candidate below:
//
//   - A symlink to `<package>/bin/codewhale.js` (npm on Unix).
//   - A shell shim in `node_modules/.bin`, beside `node_modules/codewhale`
//     (a local install, pnpm, and the aube shim that mise writes).
//   - A `.cmd` shim beside `node_modules/codewhale` (npm on Windows).
//
// The binary is NOT part of the package. The wrapper downloads it on its first
// run of each version, and writes the version it downloaded to
// `<binary>.version` (`scripts/install.js` ensureBinary). An installer such as
// mise never runs the wrapper, so a version that nobody ran yet has no binary,
// and an upgrade in place can leave the binary of the old version behind. The
// locator therefore runs a candidate only when the wrapper would run that same
// file with no download: the file is a native executable, and its marker states
// the version that the package manifest asks for.
//
// In every other case -- no candidate, a stale candidate, a native binary on
// PATH, or a path that none of the candidates explains, such as a mise shim --
// the launch runs the bare name, and the shell resolves it again at exec, as it
// does for every provider that the shared locator finds. The process-group kill
// reaches everything that the wrapper starts.
//
// Each launch chooses the program again. The resolver caches only the shell's
// answer, which is the expensive part: the path of `codewhale` on PATH. An
// upgrade replaces what that path leads to, and the next launch sees it with no
// worker restart.

// codewhaleBinaryName is the program name that the locator asks the shell for.
const codewhaleBinaryName = "codewhale"

// codewhalePackageName is the npm package directory that holds the binary, and
// the `name` that its manifest states.
const codewhalePackageName = "codewhale"

// codewhaleManifestMaxBytes limits the package manifest and the version marker
// that the resolver reads. Both are a few hundred bytes.
const codewhaleManifestMaxBytes = 64 << 10

// codewhaleResolveDeps holds the machine lookups that the resolution makes.
// Tests substitute them, because each one reads a real filesystem or runs a
// real shell.
type codewhaleResolveDeps struct {
	// resolveProgramPath returns the absolute path of a bare name in the user's
	// shell. The path is empty unless the result is ProbeYes.
	resolveProgramPath func(ctx context.Context, shellPath string, loginShell bool, name string) (string, launch.ProbeResult)
	// evalSymlinks resolves a path to the file it finally identifies.
	evalSymlinks func(string) (string, error)
	// readHead reads up to n bytes from the start of a regular file, and fails
	// for anything else.
	readHead func(path string, n int) ([]byte, error)
	// exists reports whether a path leads to a file. It follows a symlink, so a
	// link whose target is gone does not exist.
	exists func(path string) bool
	// goos selects the executable name.
	goos string
}

func codewhaleProductionDeps() codewhaleResolveDeps {
	return codewhaleResolveDeps{
		resolveProgramPath: launch.ProgramPath,
		evalSymlinks:       filepath.EvalSymlinks,
		readHead:           readRegularFileHead,
		exists:             pathExists,
		goos:               runtime.GOOS,
	}
}

// readRegularFileHead reads up to n bytes from the start of a regular file. A
// directory, a device and a missing path all fail.
func readRegularFileHead(path string, n int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:read], nil
}

// pathExists reports whether path leads to a file.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// codewhaleProductionResolver is the resolver that the registered locator
// runs. See the file comment for why a bare-name probe is not enough.
var codewhaleProductionResolver = newCodewhaleResolver(codewhaleProductionDeps())

// codewhaleLocator finds Codewhale through its own resolver.
var codewhaleLocator = launch.Custom(codewhaleProductionResolver.resolve)

// codewhaleResolver resolves how to launch Codewhale.
//
// It memoizes the shell's CONCLUSIVE answer for each shell: the availability
// scan and every launch ask the same question, and each answer spawns a login
// shell. An inconclusive answer is never cached, because it would freeze a
// transient failure as "not installed" for the worker's lifetime. The program
// that the answer leads to is chosen again at each call; see the file comment.
type codewhaleResolver struct {
	deps    codewhaleResolveDeps
	answers sync.Map // codewhaleLaunchKey -> codewhaleShellAnswer
	mutexes sync.Map // codewhaleLaunchKey -> *sync.Mutex
}

func newCodewhaleResolver(deps codewhaleResolveDeps) *codewhaleResolver {
	return &codewhaleResolver{deps: deps}
}

type codewhaleLaunchKey struct {
	shellPath  string
	loginShell bool
}

// codewhaleShellAnswer is what the shell established: the absolute path of
// `codewhale` (Found), or its absence (Missing).
type codewhaleShellAnswer struct {
	path string
	res  launch.Resolution
}

// resolve is the registered ResolverFunc. Missing means the shell ran and found
// no `codewhale`; Unknown means the shell established nothing.
func (r *codewhaleResolver) resolve(ctx context.Context, shellPath string, loginShell bool) (launch.Spec, launch.Resolution) {
	answer := r.shellAnswer(ctx, codewhaleLaunchKey{shellPath, loginShell})
	switch answer.res {
	case launch.Found:
		return launch.Spec{Program: r.program(answer.path)}, launch.Found
	case launch.Missing:
		return launch.Spec{}, launch.Missing
	default:
		return launch.Spec{}, launch.Unknown
	}
}

// shellAnswer returns the shell's answer for key: the cached one while it still
// holds, and a new probe otherwise.
func (r *codewhaleResolver) shellAnswer(ctx context.Context, key codewhaleLaunchKey) codewhaleShellAnswer {
	if answer, ok := r.cachedAnswer(key); ok {
		return answer
	}
	// Single-flight for each key. A mutex rather than a sync.Once, because an
	// attempt that ends inconclusively must be repeatable.
	muAny, _ := r.mutexes.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if answer, ok := r.cachedAnswer(key); ok {
		return answer
	}
	path, probe := r.deps.resolveProgramPath(ctx, key.shellPath, key.loginShell, codewhaleBinaryName)
	var answer codewhaleShellAnswer
	switch probe {
	case launch.ProbeYes:
		answer = codewhaleShellAnswer{path: path, res: launch.Found}
	case launch.ProbeNo:
		answer = codewhaleShellAnswer{res: launch.Missing}
	default:
		return codewhaleShellAnswer{res: launch.Unknown}
	}
	r.answers.Store(key, answer)
	return answer
}

// cachedAnswer returns the cached answer for key. A Found answer whose path no
// longer exists does not hold: an uninstall, or a `mise prune` of the version
// directory that the path runs through, removed it, and the shell now answers
// something else. The resolver drops that answer and asks the shell again.
//
// An absence stays cached for the worker's lifetime, as the shared locator
// caches it: nothing cheap can tell that a program appeared.
func (r *codewhaleResolver) cachedAnswer(key codewhaleLaunchKey) (codewhaleShellAnswer, bool) {
	v, ok := r.answers.Load(key)
	if !ok {
		return codewhaleShellAnswer{}, false
	}
	answer := v.(codewhaleShellAnswer)
	if answer.res == launch.Found && !r.deps.exists(answer.path) {
		r.answers.CompareAndDelete(key, v)
		return codewhaleShellAnswer{}, false
	}
	return answer, true
}

// program returns what the launch runs for the program that the shell found at
// path: the native binary behind the npm wrapper when the wrapper would run it
// with no download, and the bare name otherwise.
func (r *codewhaleResolver) program(path string) string {
	resolved, err := r.deps.evalSymlinks(path)
	if err != nil || resolved == "" {
		resolved = path
	}
	for _, candidate := range nativeCodewhaleCandidates(path, resolved, r.deps.goos) {
		if r.isCurrentDownload(candidate) {
			return candidate
		}
	}
	return codewhaleBinaryName
}

// nativeCodewhaleCandidates lists where the npm package keeps the native
// binary, for each way the wrapper can reach the package. See the file comment.
func nativeCodewhaleCandidates(path, resolved, goos string) []string {
	binary := codewhaleExecutableFile(goos)
	downloads := func(pkg string) string {
		return filepath.Join(pkg, "bin", "downloads", binary)
	}
	var candidates []string
	// A symlink into the package: `<package>/bin/codewhale.js`.
	if filepath.Base(filepath.Dir(resolved)) == "bin" && strings.HasSuffix(filepath.Base(resolved), ".js") {
		candidates = append(candidates, downloads(filepath.Dir(filepath.Dir(resolved))))
	}
	for _, dir := range []string{filepath.Dir(path), filepath.Dir(resolved)} {
		// A shim in `node_modules/.bin` beside `node_modules/codewhale`.
		candidates = append(candidates, downloads(filepath.Join(dir, "..", codewhalePackageName)))
		// A Windows `.cmd` shim beside `node_modules/codewhale`.
		candidates = append(candidates, downloads(filepath.Join(dir, "node_modules", codewhalePackageName)))
	}
	return candidates
}

// codewhaleExecutableFile is the native binary's file name on goos.
func codewhaleExecutableFile(goos string) string {
	if goos == "windows" {
		return codewhaleBinaryName + ".exe"
	}
	return codewhaleBinaryName
}

// isCurrentDownload reports whether the wrapper would run the binary at
// candidate as it is: the file is a native executable, and the version marker
// beside it states the binary version that the package manifest asks for.
//
// The wrapper also takes the version from CODEWHALE_VERSION and its legacy
// names. The worker cannot read the agent's shell environment here, so the
// check compares against the manifest alone. A pinned version that differs
// from the manifest makes the wrapper download it and rewrite the marker, and
// from then on this check fails and the wrapper runs.
func (r *codewhaleResolver) isCurrentDownload(candidate string) bool {
	if !isNativeExecutable(candidate, r.deps) {
		return false
	}
	want, ok := r.packageBinaryVersion(filepath.Dir(filepath.Dir(filepath.Dir(candidate))))
	if !ok {
		return false
	}
	marker, err := r.deps.readHead(candidate+".version", codewhaleManifestMaxBytes)
	return err == nil && strings.TrimSpace(string(marker)) == want
}

// codewhaleManifest is the part of the npm package manifest that states which
// binary version the wrapper runs.
type codewhaleManifest struct {
	Name                   string `json:"name"`
	Version                string `json:"version"`
	CodewhaleBinaryVersion string `json:"codewhaleBinaryVersion"`
	DeepseekBinaryVersion  string `json:"deepseekBinaryVersion"`
}

// packageBinaryVersion reads the binary version that the package at pkg asks
// for, in the precedence of the wrapper's resolvePackageVersion. ok is false for
// a directory that holds no Codewhale manifest.
func (r *codewhaleResolver) packageBinaryVersion(pkg string) (string, bool) {
	data, err := r.deps.readHead(filepath.Join(pkg, "package.json"), codewhaleManifestMaxBytes)
	if err != nil {
		return "", false
	}
	var manifest codewhaleManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Name != codewhalePackageName {
		return "", false
	}
	for _, version := range []string{manifest.CodewhaleBinaryVersion, manifest.DeepseekBinaryVersion, manifest.Version} {
		if version = strings.TrimSpace(version); version != "" {
			return version, true
		}
	}
	return "", false
}

// nativeExecutableMagics are the leading bytes of an ELF, a Mach-O (both byte
// orders, 32 and 64 bits, and a universal binary) and a PE executable. A script
// such as the Node wrapper starts with `#!` or with source text instead.
var nativeExecutableMagics = [][]byte{
	{0x7f, 'E', 'L', 'F'},
	{0xfe, 0xed, 0xfa, 0xce},
	{0xfe, 0xed, 0xfa, 0xcf},
	{0xce, 0xfa, 0xed, 0xfe},
	{0xcf, 0xfa, 0xed, 0xfe},
	{0xca, 0xfe, 0xba, 0xbe},
	{'M', 'Z'},
}

// isNativeExecutable reports whether path is a regular file that starts with a
// native executable's magic bytes.
func isNativeExecutable(path string, deps codewhaleResolveDeps) bool {
	head, err := deps.readHead(path, 4)
	if err != nil {
		return false
	}
	for _, magic := range nativeExecutableMagics {
		if bytes.HasPrefix(head, magic) {
			return true
		}
	}
	return false
}
