package memlimit

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubReadFile installs the filesystem one test means to describe, and restores
// the real one when it ends.
//
// It lives here rather than in export_test.go, where the package's other seam
// helper does: only the Linux probe reads through readFile, so an untagged home
// would leave it unused on every other GOOS -- which golangci-lint's `unused`
// fails the build over, on the platform this is developed on.
//
// No caller may be parallel: readFile is package-global and every probe reads
// through it, so a parallel sibling calling Detect would race the swap.
func stubReadFile(t *testing.T, read func(string) ([]byte, error)) {
	t.Helper()
	previous := readFile
	t.Cleanup(func() { readFile = previous })
	readFile = read
}

// stubProcFiles answers the named /proc paths from a map and lets every other
// read reach the real filesystem, which is what lets these tests describe a
// machine with /proc content while the cgroup tree itself is real files under a
// t.TempDir().
//
// A path mapped to the empty string reads as ENOENT, so a test can state
// "mountinfo is not there" as easily as it states its contents.
//
// None of its callers may be parallel -- see stubReadFile.
func stubProcFiles(t *testing.T, files map[string]string) {
	t.Helper()
	host := readFile
	stubReadFile(t, func(name string) ([]byte, error) {
		content, mapped := files[name]
		switch {
		case mapped && content == "":
			return nil, fs.ErrNotExist
		case mapped:
			return []byte(content), nil
		default:
			return host(name)
		}
	})
}

// stubMountRoot points cgroupMountRoot -- the conventional location a hierarchy
// falls back to -- at a fixture path for one test.
func stubMountRoot(t *testing.T, root string) {
	t.Helper()
	previous := cgroupMountRoot
	t.Cleanup(func() { cgroupMountRoot = previous })
	cgroupMountRoot = root
}

// stubUnmountedFallback points that fallback at a path that does not exist.
//
// A test whose fixture describes only ONE hierarchy needs this, because the
// fallback is per hierarchy: the other one stays pointed at the conventional
// location, and reads there reach the REAL /sys/fs/cgroup of whatever machine
// is running the test. A CI container with a memory limit of its own would then
// contribute a genuine -- and possibly tighter -- limit to a walk that is
// supposed to be measuring the fixture.
func stubUnmountedFallback(t *testing.T) {
	t.Helper()
	stubMountRoot(t, filepath.Join(t.TempDir(), "not-mounted"))
}

// writeLimit creates dir and the memory-limit file in it, and returns the file's
// path so a test can assert the walk proposed exactly it.
func writeLimit(t *testing.T, dir, file, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	limit := filepath.Join(dir, file)
	require.NoError(t, os.WriteFile(limit, []byte(content), 0o644))
	return limit
}

// The deployment this whole change exists for: a process confined by a
// MemoryMax= on its own cgroup, NOT inside a cgroup namespace, so the fixed root
// paths the probe used to read are the real root. v2 does not create memory.max
// there at all, so the old probe got ENOENT, reported "no limit", and Detect
// sized the queue budgets off the host's total RAM instead of the confinement.
func TestCgroupPathsFindALimitOutsideACgroupNamespace(t *testing.T) {
	// Not parallel: swaps the package-level readFile.

	const unitLimit = "/sys/fs/cgroup/system.slice/leapmux.service/memory.max"
	// Wholly fake, unlike its neighbours: this one describes the conventional
	// mount point itself, so nothing may reach the real filesystem underneath
	// it. Anything absent from the map answers ENOENT exactly as the kernel
	// does -- including mountinfo, which is what puts the conventional mount
	// point in force, and the v2 root's memory.max, which really is absent
	// there.
	stubReadFile(t, fakeFS(map[string]string{
		procSelfCgroup: "0::/system.slice/leapmux.service\n",
		unitLimit:      "268435456",
	}))

	assert.Contains(t, cgroupPaths(), unitLimit,
		"the process's own cgroup must be probed, not just the mount root")

	limit, err := cgroupLimit()
	require.NoError(t, err)
	assert.Equal(t, int64(268435456), limit)
}

// The relocated-mount deployment: cgroup2 mounted anywhere but /sys/fs/cgroup,
// which /proc/self/mountinfo knows and the hardcoded root cannot. Against real
// files and the real os.ReadFile, because path joining, a directory tree the
// walk has to climb, and a hierarchy that simply is not mounted are the half a
// readFile stub cannot cover.
func TestCgroupLimitFindsAV2HierarchyMountedElsewhere(t *testing.T) {
	// Not parallel: swaps the package-level readFile and cgroupMountRoot.
	stubUnmountedFallback(t)

	// A mount point with a space in it, so the octal escaping mountinfo applies
	// to path fields is exercised end to end rather than only in the decoder's
	// own test: undecoded, this addresses a directory that does not exist.
	mountPoint := filepath.Join(t.TempDir(), "cgroup unified")
	// The leaf carries the real limit; its parent slice and the mount root are
	// mounted but unconstrained, so the walk has to climb past two "max" files
	// to find it and must not stop at either.
	unitLimit := writeLimit(t, filepath.Join(mountPoint, "system.slice", "leapmux.service"), "memory.max", "268435456\n")
	writeLimit(t, filepath.Join(mountPoint, "system.slice"), "memory.max", "max\n")
	writeLimit(t, mountPoint, "memory.max", "max\n")

	escaped := strings.ReplaceAll(mountPoint, " ", `\040`)
	stubProcFiles(t, map[string]string{
		procSelfCgroup: "0::/system.slice/leapmux.service\n",
		// Optional fields before the "-", as a shared mount really has.
		procSelfMountinfo: "41 30 0:35 / " + escaped + " rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw,nsdelegate\n",
	})

	assert.Contains(t, cgroupPaths(), unitLimit,
		"the candidates must be rooted at the mount point mountinfo names")

	limit, err := cgroupLimit()
	require.NoError(t, err)
	assert.Equal(t, int64(268435456), limit)
}

// The v1 half of the same problem, plus the field the mount point alone does not
// carry: a mount that exposes a SUBTREE of the hierarchy. /proc/self/cgroup
// still names the cgroup by its path in the whole hierarchy, so the mount's root
// has to come off the front of it -- joined unstripped, the candidates repeat
// the prefix and address nothing.
func TestCgroupLimitFindsAV1MemoryMountExposingASubtree(t *testing.T) {
	// Not parallel: swaps the package-level readFile and cgroupMountRoot.
	stubUnmountedFallback(t)

	mountPoint := filepath.Join(t.TempDir(), "memory")
	innerLimit := writeLimit(t, filepath.Join(mountPoint, "inner"), "memory.limit_in_bytes", "134217728\n")
	writeLimit(t, mountPoint, "memory.limit_in_bytes", "9223372036854771712\n")

	stubProcFiles(t, map[string]string{
		procSelfCgroup: "12:memory:/docker/abc123/inner\n0::/\n",
		procSelfMountinfo: "44 43 0:26 /docker/abc123 " + mountPoint +
			" rw,nosuid,nodev,noexec,relatime master:14 - cgroup cgroup rw,memory\n",
	})

	paths := cgroupPaths()
	assert.Contains(t, paths, innerLimit,
		"the mount's root must be stripped from the cgroup path before it is joined on")
	assert.NotContains(t, paths, filepath.Join(mountPoint, "docker", "abc123", "inner", "memory.limit_in_bytes"),
		"joining the whole-hierarchy cgroup path onto a subtree mount repeats the prefix")

	limit, err := cgroupLimit()
	require.NoError(t, err)
	assert.Equal(t, int64(134217728), limit)
}

// cgroupMountRoot is a variable so the walk can be pointed at a fixture
// directory, and this is the test that does it -- against REAL files. It is also
// the guarantee that resolving the mount can only ever find MORE limits than the
// hardcoded path did: with mountinfo unreadable, the probe has to behave exactly
// as it did before mountinfo was consulted at all.
func TestCgroupLimitFallsBackToTheConventionalRootWithoutMountinfo(t *testing.T) {
	// Not parallel: swaps the package-level readFile and cgroupMountRoot.

	root := t.TempDir()
	unitLimit := writeLimit(t, filepath.Join(root, "system.slice", "leapmux.service"), "memory.max", "268435456\n")
	writeLimit(t, filepath.Join(root, "system.slice"), "memory.max", "max\n")
	writeLimit(t, root, "memory.max", "max\n")
	// No <root>/memory tree at all: v1 is simply not mounted, and its candidates
	// must degrade to ENOENT rather than failing the probe.
	stubMountRoot(t, root)

	stubProcFiles(t, map[string]string{
		procSelfCgroup:    "0::/system.slice/leapmux.service\n",
		procSelfMountinfo: "",
	})

	assert.Contains(t, cgroupPaths(), unitLimit,
		"an unreadable mountinfo must leave the conventional mount root in force")

	limit, err := cgroupLimit()
	require.NoError(t, err)
	assert.Equal(t, int64(268435456), limit)
}

// mountinfo that reads fine but names no cgroup mount is the same situation as
// one that cannot be read: the fallback is per hierarchy, so a host that mounts
// only one of the two still gets the conventional path for the other.
func TestCgroupPathsFallBackPerHierarchy(t *testing.T) {
	// Not parallel: swaps the package-level readFile and cgroupMountRoot.
	// Only the candidate PATHS are asserted here, so the fixture root never has
	// to exist.
	stubMountRoot(t, "/fixture/cgroup")

	t.Run("mountinfo naming no cgroup mount leaves both conventional", func(t *testing.T) {
		stubProcFiles(t, map[string]string{
			procSelfCgroup:    "0::/system.slice/leapmux.service\n",
			procSelfMountinfo: rootFSLine + "\n" + procMount + "\n",
		})

		assert.Equal(t, []string{
			"/fixture/cgroup/system.slice/leapmux.service/memory.max",
			"/fixture/cgroup/system.slice/memory.max",
			"/fixture/cgroup/memory.max",
			"/fixture/cgroup/memory/memory.limit_in_bytes",
		}, cgroupPaths())
	})

	t.Run("a resolved v2 mount does not cost v1 its fallback", func(t *testing.T) {
		stubProcFiles(t, map[string]string{
			procSelfCgroup:    "12:memory:/docker/abc\n0::/\n",
			procSelfMountinfo: "41 30 0:35 / /run/cgroup2 rw - cgroup2 cgroup2 rw\n",
		})

		assert.Equal(t, []string{
			"/run/cgroup2/memory.max",
			"/fixture/cgroup/memory/docker/abc/memory.limit_in_bytes",
			"/fixture/cgroup/memory/docker/memory.limit_in_bytes",
			"/fixture/cgroup/memory/memory.limit_in_bytes",
		}, cgroupPaths())
	})

	// A mount that exposes a subtree this process is not in holds nothing that
	// describes it. Reading that subtree's own limits would report a bound
	// belonging to whichever service it was mounted for -- likely tighter than
	// this process's own, and wrong either way.
	t.Run("a mount that cannot address this process's cgroup is not read", func(t *testing.T) {
		stubProcFiles(t, map[string]string{
			procSelfCgroup:    "0::/system.slice/leapmux.service\n",
			procSelfMountinfo: "41 30 0:35 /system.slice/other.service /mnt/other rw - cgroup2 cgroup2 rw\n",
		})

		assert.Equal(t, []string{
			"/fixture/cgroup/system.slice/leapmux.service/memory.max",
			"/fixture/cgroup/system.slice/memory.max",
			"/fixture/cgroup/memory.max",
			"/fixture/cgroup/memory/memory.limit_in_bytes",
		}, cgroupPaths(),
			"an unusable mount must degrade to the conventional root, not to a stranger's cgroup")
	})

	// ...and it must not cost this the mount that CAN address it, which is why
	// every match is kept rather than only the first.
	t.Run("a later mount that can address the cgroup is preferred", func(t *testing.T) {
		stubProcFiles(t, map[string]string{
			procSelfCgroup: "0::/system.slice/leapmux.service\n",
			procSelfMountinfo: "41 30 0:35 /system.slice/other.service /mnt/other rw - cgroup2 cgroup2 rw\n" +
				"42 30 0:35 / /run/cgroup2 rw - cgroup2 cgroup2 rw\n",
		})

		assert.Equal(t, []string{
			"/run/cgroup2/system.slice/leapmux.service/memory.max",
			"/run/cgroup2/system.slice/memory.max",
			"/run/cgroup2/memory.max",
			"/fixture/cgroup/memory/memory.limit_in_bytes",
		}, cgroupPaths())
	})
}
