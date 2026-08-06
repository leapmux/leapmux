package memlimit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real /proc/self/mountinfo lines, kept whole rather than trimmed to the fields
// the parser reads: the optional fields and the trailing mount source are
// exactly what a fixed-index parse gets wrong, so a fixture that omits them
// would agree with a broken parser.
const (
	v2Default  = "25 30 0:22 / /sys/fs/cgroup ro,nosuid,nodev,noexec shared:9 - cgroup2 cgroup2 rw,nsdelegate,memory_recursiveprot"
	v1Memory   = "31 25 0:26 / /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime shared:14 - cgroup cgroup rw,memory"
	v1CPU      = "32 25 0:27 / /sys/fs/cgroup/cpu,cpuacct rw,nosuid,nodev,noexec,relatime shared:15 - cgroup cgroup rw,cpu,cpuacct"
	procMount  = "24 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw"
	rootFSLine = "30 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw"
)

func TestCgroupMounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantV2  []cgroupMount
		wantV1  []cgroupMount
	}{
		{
			name:    "the conventional layout, which is what the hardcoded root assumed",
			content: rootFSLine + "\n" + procMount + "\n" + v2Default + "\n",
			wantV2:  []cgroupMount{{point: "/sys/fs/cgroup", root: "/"}},
		},
		{
			// The deployment this change exists for: cgroup2 mounted anywhere
			// but /sys/fs/cgroup, where the fixed path holds no limits at all
			// and the confinement went unseen.
			name:    "cgroup2 mounted somewhere other than /sys/fs/cgroup",
			content: rootFSLine + "\n" + "41 30 0:35 / /run/leapmux/cgroup2 rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup2 rw,nsdelegate\n",
			wantV2:  []cgroupMount{{point: "/run/leapmux/cgroup2", root: "/"}},
		},
		{
			// Systemd hybrid: v2 is NOT at /sys/fs/cgroup, and the memory
			// controller lives on the v1 mount beside it.
			name: "hybrid resolves each hierarchy to its own mount",
			content: "23 28 0:22 / /sys/fs/cgroup/unified rw,nosuid,nodev,noexec,relatime shared:9 - cgroup2 cgroup2 rw\n" +
				v1Memory + "\n" + v1CPU + "\n",
			wantV2: []cgroupMount{{point: "/sys/fs/cgroup/unified", root: "/"}},
			wantV1: []cgroupMount{{point: "/sys/fs/cgroup/memory", root: "/"}},
		},
		{
			// A v1 mount exposing a subtree: the mount point stands for
			// /docker/abc123, so the cgroup path has to have that prefix
			// stripped before it is joined on.
			name:    "a v1 mount that exposes a subtree keeps its root",
			content: "44 43 0:26 /docker/abc123 /sys/fs/cgroup/memory rw,relatime master:14 - cgroup cgroup rw,memory\n",
			wantV1:  []cgroupMount{{point: "/sys/fs/cgroup/memory", root: "/docker/abc123"}},
		},
		{
			name: "optional fields are counted, not assumed",
			content: "51 30 0:35 / /a rw - cgroup2 cgroup2 rw\n" +
				"52 30 0:36 / /b rw shared:9 master:2 propagate_from:2 unbindable - cgroup cgroup rw,memory\n",
			wantV2: []cgroupMount{{point: "/a", root: "/"}},
			wantV1: []cgroupMount{{point: "/b", root: "/"}},
		},
		{
			name:    "an escaped mount point is decoded",
			content: `53 30 0:35 / /var/lib/my\040cgroups rw - cgroup2 cgroup2 rw` + "\n",
			wantV2:  []cgroupMount{{point: "/var/lib/my cgroups", root: "/"}},
		},
		{
			// A v1 hierarchy NAMED memory carries no controller of that name --
			// systemd mounts one as `rw,name=systemd`. Splitting the super
			// options on commas is what tells the two apart; a substring test
			// would take this one.
			name:    "a hierarchy merely named memory is not the memory controller",
			content: "54 30 0:36 / /sys/fs/cgroup/nope rw - cgroup cgroup rw,name=memory\n",
		},
		{
			name:    "a v1 mount without the memory controller is skipped",
			content: v1CPU + "\n",
		},
		{
			name:    "no cgroup mount at all",
			content: rootFSLine + "\n" + procMount + "\n",
		},
		{
			name:    "unreadable mountinfo reads as no mounts, not as a parse failure",
			content: "",
		},
		{
			name: "lines too short to carry a separator are skipped",
			content: "garbage\n41 30 0:35 / /too-short rw\n" +
				"42 30 0:35 / /no-separator rw,nosuid cgroup2 cgroup2 rw\n" +
				"43 30 0:35 / /nothing-after rw -\n" +
				v2Default + "\n",
			wantV2: []cgroupMount{{point: "/sys/fs/cgroup", root: "/"}},
		},
		{
			// A v1 line truncated before its super options: the memory
			// controller cannot be confirmed, and reading past the end of the
			// line to look for it would panic a process that was only trying to
			// size a queue.
			name:    "a v1 line truncated before its super options is skipped",
			content: "44 30 0:36 / /sys/fs/cgroup/memory rw - cgroup\n",
		},
		{
			// Every mount of a hierarchy is kept, in mountinfo order: one that
			// exposes a subtree may not be able to address this process's cgroup
			// at all, and the caller has to be free to pass over it for one that
			// can. Keeping only the first would decide that on mount order.
			name: "every mount of a hierarchy is kept, in order",
			content: "60 30 0:22 /system.slice /mnt/bind rw - cgroup2 cgroup2 rw\n" +
				v2Default + "\n" + v1Memory + "\n" +
				"61 30 0:26 /docker/abc /mnt/memory rw - cgroup cgroup rw,memory\n",
			wantV2: []cgroupMount{
				{point: "/mnt/bind", root: "/system.slice"},
				{point: "/sys/fs/cgroup", root: "/"},
			},
			wantV1: []cgroupMount{
				{point: "/sys/fs/cgroup/memory", root: "/"},
				{point: "/mnt/memory", root: "/docker/abc"},
			},
		},
		{
			// Defensive, not a line any kernel writes: the separator is found by
			// position, so a dash among the fixed fields cannot be taken for it.
			// Scanning from the start would read the filesystem type as "/" and
			// throw the mount away.
			name:    "a dash among the fixed fields is not the separator",
			content: "61 30 - / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			wantV2:  []cgroupMount{{point: "/sys/fs/cgroup", root: "/"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v2, v1 := cgroupMounts(tt.content)
			assert.Equal(t, tt.wantV2, v2)
			assert.Equal(t, tt.wantV1, v1)
		})
	}
}

func TestCgroupMountRelMapsACgroupIntoTheMount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		mount           cgroupMount
		cgroup          string
		want            string
		wantUnreachable bool
	}{
		{
			name:   "a whole-hierarchy mount joins the cgroup path unchanged",
			mount:  cgroupMount{point: "/sys/fs/cgroup", root: "/"},
			cgroup: "/system.slice/leapmux.service",
			want:   "/system.slice/leapmux.service",
		},
		{
			name:   "a mount with no root recorded behaves the same",
			mount:  cgroupMount{point: "/sys/fs/cgroup"},
			cgroup: "/system.slice/leapmux.service",
			want:   "/system.slice/leapmux.service",
		},
		{
			// The nested-container shape: /proc/self/cgroup still names the
			// cgroup by its path in the WHOLE hierarchy, and the mount already
			// stands for the prefix. Joining it unstripped addresses
			// <point>/docker/abc/docker/abc/inner, which does not exist.
			name:   "an exposed subtree has its prefix stripped",
			mount:  cgroupMount{point: "/sys/fs/cgroup/memory", root: "/docker/abc"},
			cgroup: "/docker/abc/inner",
			want:   "/inner",
		},
		{
			name:   "the cgroup that IS the exposed subtree maps to the mount root",
			mount:  cgroupMount{point: "/sys/fs/cgroup/memory", root: "/docker/abc"},
			cgroup: "/docker/abc",
			want:   "/",
		},
		{
			// A string-prefix test would turn /docker/abcdef into /def and walk
			// a chain of directories that never existed -- or worse, into a
			// directory that exists and belongs to somebody else.
			name:            "the prefix must end on a path boundary",
			mount:           cgroupMount{point: "/sys/fs/cgroup/memory", root: "/docker/abc"},
			cgroup:          "/docker/abcdef",
			wantUnreachable: true,
		},
		{
			// Nothing under this mount describes this process's cgroup. Reading
			// the mount's own root instead would report a limit belonging to
			// whichever service the subtree was mounted for.
			name:            "a cgroup outside the exposed subtree is not addressable",
			mount:           cgroupMount{point: "/sys/fs/cgroup/memory", root: "/docker/abc"},
			cgroup:          "/system.slice/other.service",
			wantUnreachable: true,
		},
		{
			name:   "relative and unclean paths are normalised",
			mount:  cgroupMount{point: "/sys/fs/cgroup", root: "docker/abc/"},
			cgroup: "docker/abc//inner/",
			want:   "/inner",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rel, addressable := tt.mount.rel(tt.cgroup)
			if tt.wantUnreachable {
				assert.False(t, addressable)
				return
			}
			require.True(t, addressable)
			assert.Equal(t, tt.want, rel)
		})
	}
}

func TestUnescapeMountinfoPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a path with nothing to decode is returned as is",
			in:   "/sys/fs/cgroup",
			want: "/sys/fs/cgroup",
		},
		{
			name: "a space",
			in:   `/var/lib/my\040cgroups`,
			want: "/var/lib/my cgroups",
		},
		{
			name: "the other three bytes the kernel escapes",
			in:   `/a\011b\012c\134d`,
			want: "/a\tb\nc\\d",
		},
		{
			// The escape is exactly three octal digits, so a digit that follows
			// one is part of the path, not of the escape.
			name: "a digit following an escape stays in the path",
			in:   `/a\0401`,
			want: "/a 1",
		},
		{
			name: "several escapes in one field",
			in:   `/a\040b\040c`,
			want: "/a b c",
		},
		{
			// Dropping an undecodable backslash would corrupt the path rather
			// than decode it, and the result would silently address a different
			// directory.
			name: "a backslash that is not an escape is kept",
			in:   `/a\b/c\d`,
			want: `/a\b/c\d`,
		},
		{
			name: "a truncated escape at the end of the field is kept",
			in:   `/a\04`,
			want: `/a\04`,
		},
		{
			name: "a non-octal digit run is not an escape",
			in:   `/a\090b`,
			want: `/a\090b`,
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, unescapeMountinfoPath(tt.in))
		})
	}
}
