package memlimit

import "path"

// cgroupMountRoot is where the cgroup filesystems are conventionally mounted,
// used only as the FALLBACK for a hierarchy /proc/self/mountinfo could not name
// -- see cgroupPaths. A variable so the walk can be pointed at a fixture
// directory in tests; TestCgroupLimitFallsBackToTheConventionalRootWithoutMountinfo
// does exactly that, against real files, which is how the path joining and the
// climb are exercised without a stub standing in for the filesystem.
var cgroupMountRoot = "/sys/fs/cgroup"

// procSelfCgroup names the process's own cgroup membership, and
// procSelfMountinfo names where each filesystem it can see is mounted.
// Constants because the fixture tests have to fake exactly these paths while
// letting every other read reach the real filesystem, and a second literal
// there could drift from these.
const (
	procSelfCgroup    = "/proc/self/cgroup"
	procSelfMountinfo = "/proc/self/mountinfo"
)

// cgroupPaths returns the memory-limit files that bind THIS process, for both
// hierarchies, leaf first within each.
//
// Two things have to be resolved, and assuming either one is how this probe has
// been wrong before: WHERE the hierarchy is mounted, and WHICH cgroup in it
// this process belongs to.
//
// The cgroup comes from /proc/self/cgroup, and the walk climbs from it up to
// the root, because a limit set on an ancestor binds just as hard as one set on
// the leaf. Reading the fixed root paths instead -- /sys/fs/cgroup/memory.max
// and the v1 equivalent -- is right only INSIDE a cgroup namespace, where the
// container's own cgroup is remapped to appear as the root. Outside one, which
// is every systemd unit with MemoryMax= on a bare-metal host and every
// container run with --cgroupns=host, those paths are the real root: v2 does
// not even create memory.max there, so the probe failed with ENOENT, Detect
// fell through to total physical memory, and a process confined to 256 MiB
// sized its queue budgets off the host's RAM.
//
// The mount point comes from /proc/self/mountinfo, which is authoritative where
// /sys/fs/cgroup is only conventional. A runtime that mounts cgroup2 elsewhere
// -- rootless and nested setups, minimal images, a deliberately relocated
// cgroup root -- reproduced that same bug in a different place: the walk read a
// directory holding no limits and the confinement went unseen. (Systemd's
// hybrid layout was never in that group: it puts v2 at /sys/fs/cgroup/unified
// but keeps the memory controller in v1 at /sys/fs/cgroup/memory, which the
// conventional paths address correctly.)
//
// Falling back to cgroupMountRoot PER HIERARCHY, rather than for mountinfo as a
// whole, is what makes that safe to adopt: a mountinfo that is unreadable, that
// some future kernel spells differently, or that simply names no mount for one
// of the two hierarchies costs this nothing, because that hierarchy is then
// probed exactly where it was probed before mountinfo was consulted at all.
func cgroupPaths() []string {
	v2Mounts, v1Mounts := cgroupMounts(readProcFile(procSelfMountinfo))
	// An unreadable /proc/self/cgroup leaves both cgroups at "/", which is this
	// process's own cgroup inside a cgroup namespace -- the common containerised
	// case -- and harmlessly ENOENT everywhere else.
	v2Cgroup, v1Cgroup := selfCgroupPaths(readProcFile(procSelfCgroup))

	return append(
		hierarchyLimitFiles(v2Mounts, v2Cgroup, cgroupMountRoot, "memory.max"),
		hierarchyLimitFiles(v1Mounts, v1Cgroup, path.Join(cgroupMountRoot, "memory"), "memory.limit_in_bytes")...,
	)
}

// hierarchyLimitFiles lists one hierarchy's candidate limit files: the ancestor
// chain of cgroup, seen through the first mount that can address it.
//
// "Can address it" is why the mounts arrive as a list. A mount that exposes a
// subtree this process is not in cannot answer for it at all, and neither can a
// mountinfo that named no mount for this hierarchy; both land on conventional,
// where the probe read before mountinfo was consulted. A mount that CAN address
// it is believed over that convention, because the kernel's own account of
// where a filesystem is mounted beats a path that is only customary.
func hierarchyLimitFiles(mounts []cgroupMount, cgroup, conventional, file string) []string {
	for _, mount := range mounts {
		if rel, addressable := mount.rel(cgroup); addressable {
			return ancestorLimitFiles(mount.point, rel, file)
		}
	}
	return ancestorLimitFiles(conventional, cgroup, file)
}

// readProcFile reads one /proc file, reporting an unreadable one as empty
// content. Every caller here has a documented answer for "nothing is known",
// and none of them should fail a startup over a /proc that is not mounted.
func readProcFile(name string) string {
	content, err := readFile(name)
	if err != nil {
		return ""
	}
	return string(content)
}

func physicalMemory() (int64, error) {
	content, err := readFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	return parseMeminfoTotal(string(content))
}
