package zcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeStubDeps is a resolveZCodeLaunchWith substitute whose every lookup is stated by
// the test. It defaults to "nothing exists and every probe is conclusive", so a test
// states only the facts it is about.
type zcodeStubDeps struct {
	env   map[string]string
	files map[string]bool
	// nameProbe is what the shell says about a bare `zcode`. ProbeNo by default: no
	// launcher, and the probe ran.
	nameProbe launch.ProbeResult
	// paths maps a bare interpreter name to what the shell resolved it to. A name that
	// is absent from the map resolves conclusively to nothing.
	paths map[string]string
	// pathProbeFails lists the bare interpreter names whose SHELL probe proves nothing.
	pathProbeFails map[string]bool
	// interpreters maps a program path to its probe answer.
	interpreters map[string]launch.ProbeResult
	// calls records every interpreter probed, in order, so a test can assert the
	// preference order and that a resolved case probed only once.
	calls []string
}

func newZCodeStubDeps() *zcodeStubDeps {
	return &zcodeStubDeps{
		env:            map[string]string{},
		files:          map[string]bool{},
		nameProbe:      launch.ProbeNo,
		paths:          map[string]string{},
		pathProbeFails: map[string]bool{},
		interpreters:   map[string]launch.ProbeResult{},
	}
}

func (s *zcodeStubDeps) deps() zcodeResolveDeps {
	return zcodeResolveDeps{
		getenv:     func(k string) string { return s.env[k] },
		fileExists: func(p string) bool { return s.files[p] },
		probeName: func(context.Context, string, bool, string) launch.ProbeResult {
			return s.nameProbe
		},
		resolveProgramPath: func(_ context.Context, _ string, _ bool, name string) (string, launch.ProbeResult) {
			if s.pathProbeFails[name] {
				return "", launch.ProbeUnknown
			}
			if path := s.paths[name]; path != "" {
				return path, launch.ProbeYes
			}
			return "", launch.ProbeNo
		},
		probeInterpreter: func(_ context.Context, program string, _ []string) launch.ProbeResult {
			s.calls = append(s.calls, program)
			probe, ok := s.interpreters[program]
			if !ok {
				// A program the test did not list ran and rejected the script.
				return launch.ProbeNo
			}
			return probe
		},
	}
}

// resolve runs launch resolution with these stubbed lookups.
func (s *zcodeStubDeps) resolve(t *testing.T) (launch.Spec, launch.Resolution) {
	t.Helper()
	return resolveZCodeLaunchWith(context.Background(), "/bin/zsh", true, s.deps())
}

// clearZCodeLaunchCache drops every memoized launch resolution.
//
// The cache is package state keyed by shell, so a test that changes what resolution SEES
// must clear it on both sides: a stale entry would answer instead of the probe, and the
// entry this test causes would answer for the next one.
func clearZCodeLaunchCache() {
	zcodeLaunchCache.Range(func(k, _ any) bool {
		zcodeLaunchCache.Delete(k)
		return true
	})
}

// forceZCodeUnavailable makes ZCode resolve conclusively missing for the rest of the test.
//
// ZCode resolves through the FILESYSTEM, not the shell, so a developer machine with the
// desktop application installed answers for it whatever a test does to PATH or to $SHELL.
// The script override is the narrowest lever: it replaces discovery entirely, and an
// absent path is a conclusive absence -- which is what a shell-focused test needs ZCode to
// be, without stubbing the resolver it is not testing.
func forceZCodeUnavailable(t *testing.T) {
	t.Helper()
	t.Setenv(zcodeScriptEnvOverride, filepath.Join(t.TempDir(), "no-such-zcode.cjs"))
	clearZCodeLaunchCache()
	t.Cleanup(clearZCodeLaunchCache)
}

// registryWithZCodeResolver returns a registry of ZCode alone, with its launch
// resolver replaced by fn. Only the test that builds it sees the stub.
func registryWithZCodeResolver(fn launch.ResolverFunc) *agent.Registry {
	reg := Registration()
	reg.Locator = launch.Custom(fn)
	return agenttest.MustNewRegistry(reg)
}

// zcodeStubScript is a script path and the bundled interpreter derived from it, for the
// running OS -- so the test states the same relationship the resolver derives.
func zcodeStubScript() (script string, bundled []string) {
	candidates := zcodeScriptCandidates()
	script = candidates[len(candidates)-1]
	return script, bundledRuntimeCandidates(script)
}

// A `zcode` launcher on PATH wins outright: a future release that ships one needs no
// code change, and a user's own wrapper must beat the bundle.
func TestResolveZCodeLaunch_PathLauncherWins(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeYes
	script, bundled := zcodeStubScript()
	stub.files[script] = true
	for _, p := range bundled {
		stub.files[p] = true
	}

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, "zcode", spec.Program)
	assert.Empty(t, spec.PrefixArgs, "a launcher takes the app-server arguments directly")
	assert.Empty(t, spec.Env)
	assert.Empty(t, stub.calls, "a PATH launcher needs no interpreter probe at all")
}

// The bundled runtime is preferred over a `node` from the shell: it is the exact version
// ZCode itself runs the script with, so its node:sqlite behavior is the one the script
// expects.
func TestResolveZCodeLaunch_BundledRuntimeIsPreferredOverPathNode(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	require.NotEmpty(t, bundled)
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.interpreters[bundled[0]] = launch.ProbeYes
	stub.paths["node"] = "/usr/local/bin/node"
	stub.interpreters["/usr/local/bin/node"] = launch.ProbeYes

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, bundled[0], spec.Program)
	assert.Equal(t, []string{script}, spec.PrefixArgs)
	assert.Equal(t, []string{zcodeElectronAsNodeEnv}, spec.Env,
		"the bundled runtime is Electron, which starts the desktop application without this")
	assert.Equal(t, []string{bundled[0]}, stub.calls,
		"a complete installation spawns ONE subprocess and never touches the shell")
}

// ZCode 0.16.9 moved the built-in provider release beside `glm`, while its CLI
// still searches only below `glm` and at a path that resolves from the filesystem
// root. The launcher must state the real file explicitly. It must also state the
// personal file, because the CLI skips the broken discovery only when both paths
// are present.
func TestResolveZCodeLaunch_BundledScriptCarriesProviderConfigPaths(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	require.NotEmpty(t, bundled)
	config := filepath.Join(filepath.Dir(filepath.Dir(script)), "config", "provider", "zcode-builtin.json")
	stub.files[script] = true
	stub.files[config] = true
	stub.files[bundled[0]] = true
	stub.interpreters[bundled[0]] = launch.ProbeYes

	spec, res := stub.resolve(t)
	require.Equal(t, launch.Found, res)
	assert.Contains(t, spec.Env, "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE="+config)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Contains(t, spec.Env, "ZCODE_PERSONAL_PROVIDER_CONFIG_FILE="+
		filepath.Join(home, ".zcode", "v2", "provider_config.json"))
}

// An installation whose runtime was stripped still launches through a `node` from the
// LOGIN shell -- where a version manager's node lives, which the worker's own environment
// does not have.
func TestResolveZCodeLaunch_FallsBackToNodeFromTheLoginShell(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, _ := zcodeStubScript()
	stub.files[script] = true
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/opt/homebrew/bin/node"
	stub.interpreters["/opt/homebrew/bin/node"] = launch.ProbeYes

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, "/opt/homebrew/bin/node", spec.Program,
		"the ABSOLUTE path is carried forward, so the probed program is the launched program")
	assert.Equal(t, []string{script}, spec.PrefixArgs)
	assert.Empty(t, spec.Env, "a plain Node needs no ELECTRON_RUN_AS_NODE")
}

// An interpreter without node:sqlite starts and then fails inside the first RPC, which
// reads as a broken provider. Every candidate rejecting the script is an ESTABLISHED
// absence: the installation has no usable interpreter.
func TestResolveZCodeLaunch_NoInterpreterWithSQLiteIsMissing(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	stub.files[script] = true
	for _, p := range bundled {
		stub.files[p] = true
		stub.interpreters[p] = launch.ProbeNo
	}
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/usr/bin/node"
	stub.interpreters["/usr/bin/node"] = launch.ProbeNo

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Missing, res)
	assert.Empty(t, spec.Program)
	assert.Contains(t, stub.calls, "/usr/bin/node", "the shell fallback must still be tried")
}

// No script AND no launcher, with every probe conclusive, is the one authoritative
// absence.
func TestResolveZCodeLaunch_NothingInstalledIsMissing(t *testing.T) {
	t.Parallel()

	_, res := newZCodeStubDeps().resolve(t)
	assert.Equal(t, launch.Missing, res)
}

// A shell that could not start says NOTHING about whether ZCode is installed. Reporting
// missing here would freeze a broken shell as "ZCode is not installed" for the worker's
// lifetime.
func TestResolveZCodeLaunch_AnInconclusivePathProbeWithNoScriptIsUnknown(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeUnknown

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Unknown, res)
}

// An inconclusive PATH probe is irrelevant once the script is found and an interpreter
// works: the answer is established by evidence that does not depend on the shell.
func TestResolveZCodeLaunch_AnInconclusivePathProbeWithAWorkingBundleIsFound(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeUnknown
	script, bundled := zcodeStubScript()
	require.NotEmpty(t, bundled)
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.interpreters[bundled[0]] = launch.ProbeYes

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
}

// One inconclusive interpreter probe makes the WHOLE answer retryable, even when a later
// candidate answered no conclusively. Otherwise a single fork failure under load is
// cached as "ZCode is not installed".
func TestResolveZCodeLaunch_OneInconclusiveInterpreterMakesTheAnswerUnknown(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	require.NotEmpty(t, bundled)
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.interpreters[bundled[0]] = launch.ProbeUnknown
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/usr/bin/node"
	stub.interpreters["/usr/bin/node"] = launch.ProbeNo

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Unknown, res)
}

// A shell that could not resolve the interpreter NAME is inconclusive for the same
// reason, and it must not be read as "there is no node".
func TestResolveZCodeLaunch_AnInconclusiveInterpreterPathProbeIsUnknown(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, _ := zcodeStubScript()
	stub.files[script] = true
	stub.pathProbeFails[nodeInterpreterNames()[0]] = true

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Unknown, res)
}

// A resolved interpreter that is inconclusive on its own is still only one candidate --
// a LATER one that works decides. Darwin lists the Helper and then the main executable,
// so this runs there too.
func TestResolveZCodeLaunch_AWorkingCandidateAfterAnInconclusiveOneWins(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	if len(bundled) < 2 {
		t.Skipf("%s derives one bundled runtime candidate", runtime.GOOS)
	}
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.files[bundled[1]] = true
	stub.interpreters[bundled[0]] = launch.ProbeUnknown
	stub.interpreters[bundled[1]] = launch.ProbeYes

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, bundled[1], spec.Program)
}

// The PATH interpreter is the next group after the bundled runtime, and an
// inconclusive bundled probe must not hide a working node.
func TestResolveZCodeLaunch_AWorkingPathNodeAfterAnInconclusiveBundleWins(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	if len(bundled) == 0 {
		t.Skipf("%s derives no bundled runtime candidate", runtime.GOOS)
	}
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.interpreters[bundled[0]] = launch.ProbeUnknown
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/usr/bin/node"
	stub.interpreters["/usr/bin/node"] = launch.ProbeYes

	spec, res := stub.resolve(t)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, "/usr/bin/node", spec.Program)
	assert.Equal(t, []string{script}, spec.PrefixArgs)
}

// --- the overrides ---

// The script override REPLACES discovery, so a wrong value is an error the user can see
// rather than a silent fall-through to a different installation.
func TestResolveZCodeLaunch_ScriptOverrideReplacesDiscovery(t *testing.T) {
	t.Parallel()

	t.Run("a script that exists is used", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.env[zcodeScriptEnvOverride] = "  /custom/zcode.cjs  "
		stub.files["/custom/zcode.cjs"] = true
		for _, p := range bundledRuntimeCandidates("/custom/zcode.cjs") {
			stub.files[p] = true
			stub.interpreters[p] = launch.ProbeYes
		}

		spec, res := stub.resolve(t)
		assert.Equal(t, launch.Found, res)
		assert.Equal(t, []string{"/custom/zcode.cjs"}, spec.PrefixArgs, "the value is trimmed")
	})

	t.Run("a script that does not exist is missing, not a fall-through", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.env[zcodeScriptEnvOverride] = "/typo/zcode.cjs"
		stub.nameProbe = launch.ProbeYes // a launcher that WOULD have been found
		script, bundled := zcodeStubScript()
		stub.files[script] = true
		for _, p := range bundled {
			stub.files[p] = true
			stub.interpreters[p] = launch.ProbeYes
		}

		spec, res := stub.resolve(t)
		assert.Equal(t, launch.Missing, res)
		assert.Empty(t, spec.Program, "the override must not silently resolve to the bundle")
	})
}

// The interpreter override replaces DISCOVERY rather than being tried first: an operator
// who specifies an interpreter must not silently get a different one.
func TestResolveZCodeLaunch_NodeOverrideReplacesInterpreterDiscovery(t *testing.T) {
	t.Parallel()

	script, bundled := zcodeStubScript()

	t.Run("an override that works is used", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.files[script] = true
		stub.env[zcodeNodeEnvOverride] = "  /custom/node  "
		stub.interpreters["/custom/node"] = launch.ProbeYes

		spec, res := stub.resolve(t)
		assert.Equal(t, launch.Found, res)
		assert.Equal(t, "/custom/node", spec.Program)
		assert.Equal(t, []string{"/custom/node"}, stub.calls)
	})

	t.Run("an override that cannot work fails resolution", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.files[script] = true
		stub.env[zcodeNodeEnvOverride] = "/custom/node"
		stub.interpreters["/custom/node"] = launch.ProbeNo
		// A bundled runtime that WOULD have worked.
		for _, p := range bundled {
			stub.files[p] = true
			stub.interpreters[p] = launch.ProbeYes
		}

		spec, res := stub.resolve(t)
		assert.Equal(t, launch.Missing, res,
			"the specified interpreter is the answer; discovery must not run behind it")
		assert.Empty(t, spec.Program)
		assert.Equal(t, []string{"/custom/node"}, stub.calls)
	})

	// A specified interpreter that proves nothing is retryable, not missing.
	t.Run("an inconclusive override is unknown", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.files[script] = true
		stub.env[zcodeNodeEnvOverride] = "/custom/node"
		stub.interpreters["/custom/node"] = launch.ProbeUnknown

		_, res := stub.resolve(t)
		assert.Equal(t, launch.Unknown, res)
	})

	// The interpreter override alone has nothing to run, so it must not bypass the
	// script search.
	t.Run("an override with no script anywhere is missing", func(t *testing.T) {
		t.Parallel()
		stub := newZCodeStubDeps()
		stub.env[zcodeNodeEnvOverride] = "/custom/node"
		stub.interpreters["/custom/node"] = launch.ProbeYes

		_, res := stub.resolve(t)
		assert.Equal(t, launch.Missing, res)
		assert.Empty(t, stub.calls, "with no script there is nothing to probe an interpreter for")
	})
}

// --- the script and interpreter location tables ---

// A DIRECTORY at a candidate path is not a script. Handing one to the interpreter fails
// with EISDIR at launch, long after resolution claimed success.
func TestFileExists_RejectsADirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	assert.False(t, fileExists(dir))
	assert.False(t, fileExists(filepath.Join(dir, "absent")))

	file := filepath.Join(dir, "zcode.cjs")
	require.NoError(t, os.WriteFile(file, []byte("// script"), 0o600))
	assert.True(t, fileExists(file))
}

// A per-user installation is preferred over a machine-wide one: a user who installed
// their own copy means to run it.
func TestZCodeScriptCandidates_AreOrderedAndOSSpecific(t *testing.T) {
	t.Parallel()

	candidates := zcodeScriptCandidates()
	require.NotEmpty(t, candidates)
	for _, p := range candidates {
		assert.True(t, filepath.IsAbs(p), "a candidate must be an absolute path: %s", p)
		assert.Equal(t, "zcode.cjs", filepath.Base(p))
	}

	if runtime.GOOS == "darwin" {
		assert.Contains(t, candidates[len(candidates)-1], "/Applications/ZCode.app",
			"the machine-wide bundle is the last resort")
	}
}

func TestFindZCodeScript_TakesTheFirstThatExists(t *testing.T) {
	t.Parallel()

	candidates := zcodeScriptCandidates()
	if len(candidates) < 2 {
		t.Skipf("%s lists one script candidate", runtime.GOOS)
	}
	script, found := findZCodeScript(func(p string) bool { return p == candidates[1] })
	assert.True(t, found)
	assert.Equal(t, candidates[1], script)

	_, found = findZCodeScript(func(string) bool { return false })
	assert.False(t, found)
}

// The installation root is three levels above the script, which is what makes the
// bundled runtime derivable from the script path alone.
func TestBundledRuntimeCandidates_AreDerivedFromTheScriptPath(t *testing.T) {
	t.Parallel()

	script := testutil.NativeAbsPath("/tmp/install/resources/glm/zcode.cjs")
	candidates := bundledRuntimeCandidates(script)
	require.NotEmpty(t, candidates)
	for _, p := range candidates {
		assert.True(t, filepath.IsAbs(p), "a candidate must be an absolute path: %s", p)
	}
	switch runtime.GOOS {
	case "linux":
		assert.Equal(t, testutil.NativeAbsPath("/tmp/install/zcode"), candidates[0])
	case "windows":
		assert.Equal(t, []string{
			testutil.NativeAbsPath("/tmp/install/ZCode.exe"),
			testutil.NativeAbsPath("/tmp/install/zcode.exe"),
		}, candidates)
	case "darwin":
		assert.Equal(t, []string{
			testutil.NativeAbsPath("/tmp/install/Frameworks/ZCode Helper.app/Contents/MacOS/ZCode Helper"),
			testutil.NativeAbsPath("/tmp/install/MacOS/ZCode"),
		}, candidates, "the Helper is first so ELECTRON_RUN_AS_NODE does not appear in the Dock")
	}
}

// The scan walks a real Contents tree so it does not depend on /Applications.
func TestBundledDarwinRuntimeCandidates_PrefersTheHelperAndSkipsChromiumRoles(t *testing.T) {
	t.Parallel()

	contents := t.TempDir()
	helperMac := filepath.Join(contents, "Frameworks", "ZCode Helper.app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(helperMac, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(helperMac, "ZCode Helper"), []byte("x"), 0o755))

	gpuMac := filepath.Join(contents, "Frameworks", "ZCode Helper (GPU).app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(gpuMac, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gpuMac, "ZCode Helper (GPU)"), []byte("x"), 0o755))
	pluginMac := filepath.Join(contents, "Frameworks", "ZCode Helper (Plugin).app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(pluginMac, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginMac, "ZCode Helper (Plugin)"), []byte("x"), 0o755))
	rendererMac := filepath.Join(contents, "Frameworks", "ZCode Helper (Renderer).app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(rendererMac, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rendererMac, "ZCode Helper (Renderer)"), []byte("x"), 0o755))

	mainMac := filepath.Join(contents, "MacOS")
	require.NoError(t, os.MkdirAll(mainMac, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mainMac, "ZCode"), []byte("x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mainMac, ".DS_Store"), []byte("x"), 0o644))

	got := bundledDarwinRuntimeCandidates(contents)
	assert.Equal(t, []string{
		filepath.Join(helperMac, "ZCode Helper"),
		filepath.Join(mainMac, "ZCode"),
	}, got)
}

func TestBundledDarwinRuntimeCandidates_FallsBackWhenTheBundleIsAbsent(t *testing.T) {
	t.Parallel()

	contents := filepath.Join(t.TempDir(), "Contents")
	got := bundledDarwinRuntimeCandidates(contents)
	assert.Equal(t, []string{
		filepath.Join(contents, "Frameworks", "ZCode Helper.app", "Contents", "MacOS", "ZCode Helper"),
		filepath.Join(contents, "MacOS", "ZCode"),
	}, got)
}

func TestIsDarwinElectronHelperApp(t *testing.T) {
	t.Parallel()

	assert.True(t, isDarwinElectronHelperApp("ZCode Helper.app"))
	assert.True(t, isDarwinElectronHelperApp("Foo Helper.app"))
	assert.False(t, isDarwinElectronHelperApp("ZCode Helper (GPU).app"))
	assert.False(t, isDarwinElectronHelperApp("ZCode Helper (Plugin).app"))
	assert.False(t, isDarwinElectronHelperApp("ZCode Helper (Renderer).app"))
	// HasSuffix(" Helper.app") is true here; the paren check is what rejects it.
	assert.False(t, isDarwinElectronHelperApp("ZCode (Canary) Helper.app"))
	assert.False(t, isDarwinElectronHelperApp("ZCode.app"))
	assert.False(t, isDarwinElectronHelperApp("Electron Framework.framework"))
	assert.False(t, isDarwinElectronHelperApp(""))
	assert.False(t, isDarwinElectronHelperApp("Helper.app"))
}

func TestDarwinHelperExecutable_FallsBackToTheBundleStem(t *testing.T) {
	t.Parallel()

	helperApp := filepath.Join(t.TempDir(), "ZCode Helper.app")
	require.NoError(t, os.MkdirAll(filepath.Join(helperApp, "Contents", "MacOS"), 0o755))
	assert.Equal(t, filepath.Join(helperApp, "Contents", "MacOS", "ZCode Helper"),
		darwinHelperExecutable(helperApp))
}

// The Helper is probed before the main executable: both work as Node, and the Helper
// is the one that stays out of the Dock.
func TestResolveZCodeLaunch_BundledHelperIsPreferredOverTheMainExecutable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the Dock-hidden Helper is a macOS bundle")
	}

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	if len(bundled) < 2 || !strings.Contains(bundled[0], "Helper") {
		t.Skip("this install does not list the UI-element Helper first")
	}
	stub.files[script] = true
	for _, p := range bundled {
		stub.files[p] = true
		stub.interpreters[p] = launch.ProbeYes
	}

	spec, res := stub.resolve(t)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, bundled[0], spec.Program)
	assert.Equal(t, []string{bundled[0]}, stub.calls,
		"the first working candidate stops the search")
}

// A Helper that is not on disk is skipped; the main executable still launches.
func TestResolveZCodeLaunch_MissingHelperFallsBackToTheMainExecutable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the Dock-hidden Helper is a macOS bundle")
	}

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	if len(bundled) < 2 {
		t.Skip("this install lists fewer than two bundled runtimes")
	}
	stub.files[script] = true
	stub.files[bundled[1]] = true
	stub.interpreters[bundled[1]] = launch.ProbeYes

	spec, res := stub.resolve(t)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, bundled[1], spec.Program)
	assert.Equal(t, []string{bundled[1]}, stub.calls)
}

// A Helper that exists and cannot run node:sqlite is a conclusive no, not a missing
// file. The main executable is still the fallback.
func TestResolveZCodeLaunch_HelperThatCannotRunSQLiteFallsBackToTheMainExecutable(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("the Dock-hidden Helper is a macOS bundle")
	}

	stub := newZCodeStubDeps()
	script, bundled := zcodeStubScript()
	if len(bundled) < 2 || !strings.Contains(bundled[0], "Helper") {
		t.Skip("this install does not list the UI-element Helper first")
	}
	stub.files[script] = true
	stub.files[bundled[0]] = true
	stub.files[bundled[1]] = true
	stub.interpreters[bundled[0]] = launch.ProbeNo
	stub.interpreters[bundled[1]] = launch.ProbeYes

	spec, res := stub.resolve(t)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, bundled[1], spec.Program)
	assert.Equal(t, []string{bundled[0], bundled[1]}, stub.calls)
}

// Only a runtime that EXISTS becomes a candidate, and each one carries the Electron
// environment it needs.
func TestBundledInterpreters_OnlyIncludesWhatExists(t *testing.T) {
	t.Parallel()

	script, bundled := zcodeStubScript()
	require.NotEmpty(t, bundled)

	interpreters := bundledInterpreters(script, func(p string) bool { return p == bundled[0] })
	require.Len(t, interpreters, 1)
	assert.Equal(t, bundled[0], interpreters[0].program)
	assert.Equal(t, []string{zcodeElectronAsNodeEnv}, interpreters[0].env)

	assert.Empty(t, bundledInterpreters(script, func(string) bool { return false }))
}

// --- the shell probe parser ---

// --- the cache ---

// Only a CONCLUSIVE resolution is cached. Caching Unknown would freeze a
// load-induced timeout as a permanent verdict for the worker's lifetime.
func TestResolveZCodeLaunch_CachesOnlyAConclusiveResolution(t *testing.T) {
	// Not parallel: the cache is package state, keyed by shell.
	shell := "/bin/zsh-cache-test"
	t.Cleanup(func() {
		zcodeLaunchCache.Delete(zcodeLaunchKey{shell, true})
		zcodeLaunchMutexes.Delete(zcodeLaunchKey{shell, true})
	})

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeUnknown
	_, res := resolveZCodeLaunchWith(context.Background(), shell, true, stub.deps())
	require.Equal(t, launch.Unknown, res)
	_, cached := zcodeLaunchCache.Load(zcodeLaunchKey{shell, true})
	assert.False(t, cached, "an inconclusive resolution must stay retryable")

	// The production entry point caches a conclusive answer under the same key.
	zcodeLaunchCache.Store(zcodeLaunchKey{shell, true}, zcodeLaunchCacheEntry{
		spec: launch.Spec{Program: "zcode"}, res: launch.Found,
	})
	spec, res := resolveZCodeLaunch(context.Background(), shell, true)
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, "zcode", spec.Program, "the cached entry is served without a probe")
}

// --- the registration ---

// The resolver must be REGISTERED, or ZCode falls back to probing a bare `zcode` name
// that no installation provides, and the provider never appears as available.
// TestRegistryCoversEveryProvider, in the composition root, pins that ZCode's
// registration is in the worker's registry.
func TestZCodeLaunchResolverIsRegistered(t *testing.T) {
	entry := Registration()
	require.True(t, entry.Locator.Valid())

	// The two kinds of locator answer a shell that cannot start differently, which
	// tells them apart: a bare-name probe establishes nothing there, and ZCode's
	// resolver, pointed at an absent script, answers from the filesystem alone.
	forceZCodeUnavailable(t)
	missingShell := filepath.Join(t.TempDir(), "no-such-shell")
	assert.Equal(t, launch.Missing, entry.Locator.Available(context.Background(), missingShell, false),
		"ZCode ships no PATH binary, so its locator must be its launch resolver")
}

// An inconclusive PATH probe is not settled by finding a script. If no interpreter
// works either, nothing ruled out a working `zcode` on PATH -- and resolveZCodeLaunch
// CACHES a Missing for the worker's life, so freezing one here would report ZCode
// as not installed until the worker restarts.
func TestResolveZCodeLaunch_AnInconclusivePathProbeWithNoInterpreterIsUnknown(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeUnknown // a shell that would not start: the PATH probe proved nothing
	script, bundled := zcodeStubScript()
	stub.files[script] = true
	for _, p := range bundled {
		stub.files[p] = true
		stub.interpreters[p] = launch.ProbeNo
	}
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/usr/bin/node"
	stub.interpreters["/usr/bin/node"] = launch.ProbeNo

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Unknown, res,
		"a PATH launcher was never ruled out, so the absence is not authoritative and must be retried")
}

// The same shape with a CONCLUSIVE PATH probe is a real absence: every probe ran, so
// caching the verdict is correct. This is the twin that keeps the fix above from simply
// turning every missing install into an endless retry.
func TestResolveZCodeLaunch_AConclusivePathProbeWithNoInterpreterIsMissing(t *testing.T) {
	t.Parallel()

	stub := newZCodeStubDeps()
	stub.nameProbe = launch.ProbeNo
	script, bundled := zcodeStubScript()
	stub.files[script] = true
	for _, p := range bundled {
		stub.files[p] = true
		stub.interpreters[p] = launch.ProbeNo
	}
	name := nodeInterpreterNames()[0]
	stub.paths[name] = "/usr/bin/node"
	stub.interpreters["/usr/bin/node"] = launch.ProbeNo

	_, res := stub.resolve(t)
	assert.Equal(t, launch.Missing, res)
}

// A provider with a launch resolver is answered by that resolver, not by a PATH probe --
// and the resolver's three states must reach Registry.ListAvailable unchanged.
func TestListAvailableProvidersUsesTheLaunchResolver(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell on this machine")
	}
	tests := []struct {
		name         string
		resolution   launch.Resolution
		wantListed   bool
		wantComplete bool
	}{
		{name: "found is listed", resolution: launch.Found, wantListed: true, wantComplete: true},
		{name: "missing is a complete absence", resolution: launch.Missing, wantComplete: true},
		{
			// The load-bearing state: a resolver that established nothing must make the
			// whole scan retryable, exactly as an inconclusive PATH probe does.
			name:       "unknown makes the scan incomplete",
			resolution: launch.Unknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := registryWithZCodeResolver(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
				return launch.Spec{Program: "/opt/node", PrefixArgs: []string{"/opt/zcode.cjs"}}, tc.resolution
			})

			providers, complete := r.ListAvailable(context.Background(), shell, false)

			assert.Equal(t, tc.wantComplete, complete)
			assert.Equal(t, tc.wantListed,
				slices.Contains(providers, leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE))
		})
	}
}
