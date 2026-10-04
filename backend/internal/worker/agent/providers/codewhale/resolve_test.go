package codewhale

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// resolveStub is a filesystem and a shell that a resolution test states.
type resolveStub struct {
	probe launch.ProbeResult
	path  string
	// links maps a path to the file it finally identifies.
	links map[string]string
	// natives lists the files that start with a native executable's magic.
	natives map[string]bool
	// files maps every other regular file to its content: a package manifest and
	// a version marker.
	files map[string]string
	goos  string
}

func (s resolveStub) deps() codewhaleResolveDeps {
	return codewhaleResolveDeps{
		resolveProgramPath: func(context.Context, string, bool, string) (string, launch.ProbeResult) {
			if s.probe != launch.ProbeYes {
				return "", s.probe
			}
			return s.path, s.probe
		},
		evalSymlinks: func(path string) (string, error) {
			if target, ok := s.links[path]; ok {
				return target, nil
			}
			return path, nil
		},
		readHead: func(path string, n int) ([]byte, error) {
			if s.natives[path] {
				return []byte{0x7f, 'E', 'L', 'F'}, nil
			}
			if content, ok := s.files[path]; ok {
				return []byte(content[:min(n, len(content))]), nil
			}
			if _, ok := s.links[path]; ok || path == s.path {
				return []byte("#!/usr/bin/env node"), nil
			}
			return nil, os.ErrNotExist
		},
		exists: func(path string) bool {
			_, linked := s.links[path]
			_, file := s.files[path]
			return path == s.path || linked || s.natives[path] || file
		},
		goos: s.goos,
	}
}

// npmDownload states an npm package at pkg that asks for version, and the native
// binary that the wrapper downloaded for marker. The binary's file name follows
// goos.
func npmDownload(pkg, version, marker, goos string) (native string, natives map[string]bool, files map[string]string) {
	native = filepath.Join(pkg, "bin", "downloads", codewhaleExecutableFile(goos))
	return native, map[string]bool{native: true}, map[string]string{
		filepath.Join(pkg, "package.json"): `{"name":"codewhale","version":"` + version + `"}`,
		native + ".version":                marker,
	}
}

func resolveWith(stub resolveStub) (launch.Spec, launch.Resolution) {
	return newCodewhaleResolver(stub.deps()).resolve(context.Background(), "/bin/zsh", true)
}

func TestResolveCodewhaleLaunchReportsTheShellsAnswer(t *testing.T) {
	t.Parallel()
	_, res := resolveWith(resolveStub{probe: launch.ProbeNo})
	assert.Equal(t, launch.Missing, res)
	_, res = resolveWith(resolveStub{probe: launch.ProbeUnknown})
	assert.Equal(t, launch.Unknown, res)
}

func TestResolveCodewhaleLaunchLetsTheShellRunANativeBinaryOnPath(t *testing.T) {
	t.Parallel()
	// The native binary itself is on PATH, directly or through a link. The shell
	// resolves the bare name to it again at exec, and to its replacement after an
	// upgrade.
	spec, res := resolveWith(resolveStub{probe: launch.ProbeYes, path: "/opt/bin/codewhale", natives: map[string]bool{"/opt/bin/codewhale": true}})
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, codewhaleBinaryName, spec.Program)

	spec, _ = resolveWith(resolveStub{
		probe: launch.ProbeYes, path: "/usr/local/bin/codewhale",
		links:   map[string]string{"/usr/local/bin/codewhale": "/opt/codewhale/codewhale"},
		natives: map[string]bool{"/opt/codewhale/codewhale": true},
	})
	assert.Equal(t, codewhaleBinaryName, spec.Program)
}

func TestResolveCodewhaleLaunchLooksPastTheNpmWrapper(t *testing.T) {
	t.Parallel()
	// npm on Unix: a link to the package's own script.
	pkg := "/home/u/.npm/lib/node_modules/codewhale"
	native, natives, files := npmDownload(pkg, "0.10.0", "0.10.0", "linux")
	spec, res := resolveWith(resolveStub{
		probe: launch.ProbeYes, path: "/home/u/.npm/bin/codewhale",
		links:   map[string]string{"/home/u/.npm/bin/codewhale": filepath.Join(pkg, "bin", "codewhale.js")},
		natives: natives, files: files,
	})
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, native, spec.Program)

	// A shim in node_modules/.bin, beside the package.
	shim := "/w/node_modules/.bin/codewhale"
	native, natives, files = npmDownload("/w/node_modules/codewhale", "0.10.0", "0.10.0", "linux")
	spec, _ = resolveWith(resolveStub{probe: launch.ProbeYes, path: shim, links: map[string]string{shim: shim}, natives: natives, files: files})
	assert.Equal(t, native, spec.Program)

	// A Windows shim beside node_modules. The marker follows the `.exe` name.
	cmd := `C:\npm\codewhale.cmd`
	native, natives, files = npmDownload(filepath.Join(filepath.Dir(cmd), "node_modules", "codewhale"), "0.10.0", "0.10.0", "windows")
	assert.Equal(t, "codewhale.exe", filepath.Base(native))
	spec, _ = resolveWith(resolveStub{probe: launch.ProbeYes, path: cmd, links: map[string]string{cmd: cmd}, natives: natives, files: files, goos: "windows"})
	assert.Equal(t, native, spec.Program)
}

// The wrapper runs its download as it is only when the download is the version
// that the package asks for. For anything else it downloads first, so the launch
// runs the wrapper.
func TestResolveCodewhaleLaunchRunsTheWrapperForADownloadItCannotVouchFor(t *testing.T) {
	t.Parallel()
	shim := "/w/node_modules/.bin/codewhale"
	pkg := "/w/node_modules/codewhale"
	manifest := filepath.Join(pkg, "package.json")
	resolveDownload := func(t *testing.T, edit func(native string, natives map[string]bool, files map[string]string)) string {
		t.Helper()
		native, natives, files := npmDownload(pkg, "0.10.0", "0.10.0", "linux")
		edit(native, natives, files)
		spec, res := resolveWith(resolveStub{probe: launch.ProbeYes, path: shim, natives: natives, files: files})
		require.Equal(t, launch.Found, res)
		return spec.Program
	}

	cases := map[string]func(native string, natives map[string]bool, files map[string]string){
		"no binary": func(native string, natives map[string]bool, files map[string]string) {
			delete(natives, native)
			delete(files, native+".version")
		},
		"a binary with no marker": func(native string, _ map[string]bool, files map[string]string) {
			delete(files, native+".version")
		},
		"a binary of another version": func(native string, _ map[string]bool, files map[string]string) {
			files[native+".version"] = "0.9.13"
		},
		"a script where the binary goes": func(native string, natives map[string]bool, files map[string]string) {
			delete(natives, native)
			files[native] = "#!/bin/sh\n"
		},
		"no manifest": func(_ string, _ map[string]bool, files map[string]string) {
			delete(files, manifest)
		},
		"a manifest of another package": func(_ string, _ map[string]bool, files map[string]string) {
			files[manifest] = `{"name":"codewhale-fork","version":"0.10.0"}`
		},
		"a manifest that states no version": func(_ string, _ map[string]bool, files map[string]string) {
			files[manifest] = `{"name":"codewhale"}`
		},
		"a manifest that is not JSON": func(_ string, _ map[string]bool, files map[string]string) {
			files[manifest] = `{"name":`
		},
		"a binary version that overrides the package version": func(_ string, _ map[string]bool, files map[string]string) {
			files[manifest] = `{"name":"codewhale","version":"0.10.0","codewhaleBinaryVersion":"0.10.1"}`
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, codewhaleBinaryName, resolveDownload(t, edit))
		})
	}

	t.Run("the binary version that the manifest states", func(t *testing.T) {
		t.Parallel()
		native := ""
		program := resolveDownload(t, func(n string, _ map[string]bool, files map[string]string) {
			native = n
			files[manifest] = `{"name":"codewhale","version":"0.10.0","codewhaleBinaryVersion":"0.10.1"}`
			files[n+".version"] = "0.10.1\n"
		})
		assert.Equal(t, native, program)
	})

	t.Run("the legacy binary version", func(t *testing.T) {
		t.Parallel()
		native := ""
		program := resolveDownload(t, func(n string, _ map[string]bool, files map[string]string) {
			native = n
			files[manifest] = `{"name":"codewhale","version":"0.10.0","deepseekBinaryVersion":"0.9.9"}`
			files[n+".version"] = "0.9.9"
		})
		assert.Equal(t, native, program)
	})
}

func TestResolveCodewhaleLaunchLetsTheShellRunAPathNothingExplains(t *testing.T) {
	t.Parallel()
	// A mise shim is a link to mise itself, and no candidate holds a binary.
	spec, res := resolveWith(resolveStub{
		probe: launch.ProbeYes, path: "/home/u/.local/share/mise/shims/codewhale",
		links: map[string]string{"/home/u/.local/share/mise/shims/codewhale": "/usr/bin/mise"},
	})
	assert.Equal(t, launch.Found, res)
	assert.Equal(t, codewhaleBinaryName, spec.Program)
}

func TestIsNativeExecutable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name string, head []byte) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, head, 0o755))
		return path
	}
	deps := codewhaleProductionDeps()
	assert.True(t, isNativeExecutable(write("elf", []byte{0x7f, 'E', 'L', 'F', 2}), deps))
	assert.True(t, isNativeExecutable(write("macho", []byte{0xcf, 0xfa, 0xed, 0xfe}), deps))
	assert.True(t, isNativeExecutable(write("universal", []byte{0xca, 0xfe, 0xba, 0xbe}), deps))
	assert.True(t, isNativeExecutable(write("pe", []byte{'M', 'Z', 0x90, 0}), deps))
	assert.False(t, isNativeExecutable(write("script", []byte("#!/bin/sh\n")), deps))
	assert.False(t, isNativeExecutable(write("short", []byte{0x7f}), deps))
	assert.False(t, isNativeExecutable(dir, deps), "a directory is not an executable")
	assert.False(t, isNativeExecutable(filepath.Join(dir, "absent"), deps))
}

func TestCodewhaleExecutableFile(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "codewhale", codewhaleExecutableFile("darwin"))
	assert.Equal(t, "codewhale.exe", codewhaleExecutableFile("windows"))
}

// clearCodewhaleLaunchCache drops every memoized answer of the production
// resolver. The cache is package state keyed by shell, so a test that changes
// what resolution SEES clears it on both sides.
func clearCodewhaleLaunchCache() {
	codewhaleProductionResolver.answers.Range(func(key, _ any) bool {
		codewhaleProductionResolver.answers.Delete(key)
		return true
	})
}

// countProbes makes a stub's shell count the probes it answers.
func countProbes(stub *resolveStub, probes *atomic.Int32) codewhaleResolveDeps {
	deps := stub.deps()
	deps.resolveProgramPath = func(ctx context.Context, shellPath string, loginShell bool, name string) (string, launch.ProbeResult) {
		probes.Add(1)
		return stub.deps().resolveProgramPath(ctx, shellPath, loginShell, name)
	}
	return deps
}

// Only a CONCLUSIVE answer is cached: caching Unknown would freeze a transient
// failure as "not installed" for the worker's lifetime.
func TestResolveCodewhaleLaunchCachesAConclusiveAnswer(t *testing.T) {
	t.Parallel()
	var probes atomic.Int32
	stub := &resolveStub{probe: launch.ProbeUnknown}
	resolver := newCodewhaleResolver(countProbes(stub, &probes))

	_, res := resolver.resolve(context.Background(), "/bin/zsh", true)
	assert.Equal(t, launch.Unknown, res)
	_, _ = resolver.resolve(context.Background(), "/bin/zsh", true)
	assert.Equal(t, int32(2), probes.Load(), "an inconclusive answer is asked again")

	stub.probe = launch.ProbeNo
	_, res = resolver.resolve(context.Background(), "/bin/zsh", true)
	assert.Equal(t, launch.Missing, res)
	_, res = resolver.resolve(context.Background(), "/bin/zsh", true)
	assert.Equal(t, launch.Missing, res)
	assert.Equal(t, int32(3), probes.Load(), "a conclusive answer is kept")

	// Each shell has its own answer.
	_, _ = resolver.resolve(context.Background(), "/bin/zsh", false)
	assert.Equal(t, int32(4), probes.Load())
}

func TestResolveCodewhaleLaunchAsksTheShellOnceForConcurrentCallers(t *testing.T) {
	t.Parallel()
	var probes atomic.Int32
	stub := &resolveStub{probe: launch.ProbeYes, path: "/opt/bin/codewhale"}
	resolver := newCodewhaleResolver(countProbes(stub, &probes))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, res := resolver.resolve(context.Background(), "/bin/zsh", true)
			assert.Equal(t, launch.Found, res)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), probes.Load())
}

func TestCodewhaleLocatorIsRegistered(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Locator.Valid())
}

// npmInstall is an npm install of Codewhale on disk: the shell shim in
// `node_modules/.bin`, and the package with its manifest.
type npmInstall struct {
	shim string
	pkg  string
}

func newNpmInstall(t *testing.T, version string) npmInstall {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node_modules")
	install := npmInstall{shim: filepath.Join(root, ".bin", "codewhale"), pkg: filepath.Join(root, "codewhale")}
	require.NoError(t, os.MkdirAll(filepath.Dir(install.shim), 0o755))
	require.NoError(t, os.WriteFile(install.shim, []byte("#!/bin/sh\nexec node ../codewhale/bin/codewhale.js \"$@\"\n"), 0o755))
	install.setVersion(t, version)
	return install
}

// setVersion writes the package manifest, as an upgrade in place does.
func (i npmInstall) setVersion(t *testing.T, version string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(i.pkg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(i.pkg, "package.json"), mustJSON(t, map[string]any{"name": "codewhale", "version": version}), 0o644))
}

// native is where the wrapper downloads the binary.
func (i npmInstall) native() string {
	return filepath.Join(i.pkg, "bin", "downloads", codewhaleExecutableFile(runtime.GOOS))
}

// download writes the binary and its version marker, as the wrapper does on its
// first run of a version.
func (i npmInstall) download(t *testing.T, version string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(i.native()), 0o755))
	require.NoError(t, os.WriteFile(i.native(), []byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, 0o755))
	require.NoError(t, os.WriteFile(i.native()+".version", []byte(version), 0o644))
}

// removeDownload empties `bin/downloads`, which is the state of a version that
// mise installed and nobody ran yet.
func (i npmInstall) removeDownload(t *testing.T) {
	t.Helper()
	require.NoError(t, os.RemoveAll(filepath.Dir(i.native())))
}

// resolverOnDisk resolves against the real filesystem, with a shell that
// answers *path and counts its probes.
func resolverOnDisk(path *string, probes *atomic.Int32) *codewhaleResolver {
	deps := codewhaleProductionDeps()
	deps.resolveProgramPath = func(context.Context, string, bool, string) (string, launch.ProbeResult) {
		probes.Add(1)
		return *path, launch.ProbeYes
	}
	return newCodewhaleResolver(deps)
}

// An upgrade must need no worker restart: the native binary that one launch
// ran can be gone at the next one, because a new version downloads its binary
// only when the wrapper first runs.
func TestResolveCodewhaleLaunchChoosesTheNativeBinaryAtEachLaunch(t *testing.T) {
	t.Parallel()
	install := newNpmInstall(t, "0.10.0")
	install.download(t, "0.10.0")
	var probes atomic.Int32
	resolver := resolverOnDisk(&install.shim, &probes)
	resolve := func() string {
		spec, res := resolver.resolve(context.Background(), "/bin/zsh", true)
		require.Equal(t, launch.Found, res)
		return spec.Program
	}

	assert.Equal(t, install.native(), resolve())

	// The upgrade: the new version's binary is not downloaded yet. The wrapper
	// downloads it, so the launch runs the wrapper through the shell.
	install.setVersion(t, "0.11.0")
	install.removeDownload(t)
	assert.Equal(t, codewhaleBinaryName, resolve())

	// A binary of the old version is still in place: the wrapper would replace it
	// before it runs anything, so the launch runs the wrapper.
	install.download(t, "0.10.0")
	assert.Equal(t, codewhaleBinaryName, resolve())

	// The wrapper downloaded the new version.
	install.download(t, "0.11.0")
	assert.Equal(t, install.native(), resolve())
	assert.Equal(t, int32(1), probes.Load(), "the shell's answer still holds, so the shell is not asked again")
}

// A shell answer whose program is gone -- an uninstall, or a pruned version
// directory -- is asked again rather than launched.
func TestResolveCodewhaleLaunchAsksTheShellAgainWhenItsAnswerIsGone(t *testing.T) {
	t.Parallel()
	old := newNpmInstall(t, "0.10.0")
	old.download(t, "0.10.0")
	answer := old.shim
	var probes atomic.Int32
	resolver := resolverOnDisk(&answer, &probes)

	spec, res := resolver.resolve(context.Background(), "/bin/zsh", true)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, old.native(), spec.Program)

	current := newNpmInstall(t, "0.11.0")
	current.download(t, "0.11.0")
	answer = current.shim
	require.NoError(t, os.RemoveAll(filepath.Dir(old.pkg)))

	spec, res = resolver.resolve(context.Background(), "/bin/zsh", true)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, current.native(), spec.Program)
	assert.Equal(t, int32(2), probes.Load())
}

func TestReadRegularFileHead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest")
	require.NoError(t, os.WriteFile(path, []byte("abcdef"), 0o644))

	head, err := readRegularFileHead(path, 4)
	require.NoError(t, err)
	assert.Equal(t, []byte("abcd"), head, "the read stops at the limit")
	head, err = readRegularFileHead(path, 64)
	require.NoError(t, err)
	assert.Equal(t, []byte("abcdef"), head, "a file shorter than the limit reads whole")

	_, err = readRegularFileHead(dir, 4)
	assert.ErrorIs(t, err, os.ErrInvalid, "a directory is not a regular file")
	_, err = readRegularFileHead(filepath.Join(dir, "absent"), 4)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// A path whose links the resolver cannot resolve still leads to the shim's own
// candidates.
func TestResolveCodewhaleLaunchUsesThePathWhenItsLinksCannotBeResolved(t *testing.T) {
	t.Parallel()
	shim := "/w/node_modules/.bin/codewhale"
	native, natives, files := npmDownload("/w/node_modules/codewhale", "0.10.0", "0.10.0", "linux")
	deps := resolveStub{probe: launch.ProbeYes, path: shim, natives: natives, files: files}.deps()
	deps.evalSymlinks = func(string) (string, error) { return "", os.ErrPermission }

	spec, res := newCodewhaleResolver(deps).resolve(context.Background(), "/bin/zsh", true)
	require.Equal(t, launch.Found, res)
	assert.Equal(t, native, spec.Program)
}
