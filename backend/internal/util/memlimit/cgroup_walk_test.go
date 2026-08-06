package memlimit

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFS answers the walk from a map, and reports ENOENT for anything absent --
// which is what a real host does for the hierarchy it does not mount.
func fakeFS(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		content, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(content), nil
	}
}

// The walk is the whole reason cgroup detection has a seam, and nothing
// exercised it: stubProbes replaces the probes one level ABOVE it, so on darwin
// (where cgroupPaths is empty) the body never ran at all, and on Linux CI only
// whatever files that host happened to have were read.
func TestReadCgroupLimitTakesTheTightestLimitOnTheChain(t *testing.T) {
	t.Parallel()

	const (
		leaf   = "/sys/fs/cgroup/system.slice/leapmux.service/memory.max"
		parent = "/sys/fs/cgroup/system.slice/memory.max"
		root   = "/sys/fs/cgroup/memory.max"
	)

	t.Run("an ancestor limit binds even when the leaf sets none", func(t *testing.T) {
		t.Parallel()
		limit, err := readCgroupLimit([]string{leaf, parent, root}, fakeFS(map[string]string{
			leaf:   "max",
			parent: "268435456",
			root:   "max",
		}))
		require.NoError(t, err)
		assert.Equal(t, int64(268435456), limit)
	})

	t.Run("the tightest wins, not the first", func(t *testing.T) {
		t.Parallel()
		// Leaf-first order, so "first" and "tightest" disagree: a walk that
		// stopped at the first real limit would report the 1 GiB parent and size
		// four times the queue memory the 256 MiB leaf actually allows.
		limit, err := readCgroupLimit([]string{parent, leaf}, fakeFS(map[string]string{
			parent: "1073741824",
			leaf:   "268435456",
		}))
		require.NoError(t, err)
		assert.Equal(t, int64(268435456), limit)
	})

	t.Run("a hierarchy that reads fine but imposes nothing does not stop the walk", func(t *testing.T) {
		t.Parallel()
		// The hybrid host: v2 mounted and unconstrained, the real limit on v1.
		limit, err := readCgroupLimit([]string{root, "/sys/fs/cgroup/memory/memory.limit_in_bytes"}, fakeFS(map[string]string{
			root: "max",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "134217728",
		}))
		require.NoError(t, err)
		assert.Equal(t, int64(134217728), limit)
	})

	t.Run("v1's unlimited sentinel is not a limit", func(t *testing.T) {
		t.Parallel()
		_, err := readCgroupLimit([]string{"/v1"}, fakeFS(map[string]string{
			"/v1": "9223372036854771712",
		}))
		assert.ErrorIs(t, err, errNoLimit)
	})

	t.Run("no candidates at all reports no limit, not a read failure", func(t *testing.T) {
		t.Parallel()
		_, err := readCgroupLimit(nil, fakeFS(nil))
		assert.ErrorIs(t, err, errNoLimit)
	})

	// The three error arms used to disagree: two kept the first error, the
	// errNoLimit arm overwrote unconditionally -- so a genuine read failure was
	// silently replaced by "nothing is configured", which sends an operator
	// looking in a different place.
	t.Run("keeps the first error rather than the last", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("permission denied")
		_, err := readCgroupLimit([]string{"/broken", "/unlimited"}, func(path string) ([]byte, error) {
			if path == "/broken" {
				return nil, boom
			}
			return []byte("max"), nil
		})
		assert.ErrorIs(t, err, boom)
	})

	// An absent candidate is a statement about the machine, not a failure of the
	// probe: the hierarchy is not mounted, or this level of it has no limit
	// file, which on v2's root cgroup is every unconstrained Linux host there
	// is. Reported as a failure, it makes Detect annotate a perfectly healthy
	// host with "cgroup probe failed" on every start.
	t.Run("candidates that do not exist report no limit, not a failure", func(t *testing.T) {
		t.Parallel()
		_, err := readCgroupLimit([]string{"/v2/memory.max", "/v1/memory.limit_in_bytes"}, fakeFS(nil))
		assert.ErrorIs(t, err, errNoLimit)
		assert.NotErrorIs(t, err, fs.ErrNotExist,
			"an unmounted hierarchy is not a diagnosis worth showing an operator")
	})

	// The masked-diagnosis case the "first error" rule alone does not cover: the
	// candidates are leaf first across BOTH hierarchies, so a v2 leaf reading
	// "max" comes before the v1 file that could not be read at all, and keeping
	// the first error uniformly would report the machine as unconstrained.
	t.Run("a real failure is not masked by an earlier absent or unlimited file", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("permission denied")
		_, err := readCgroupLimit([]string{"/v2/leaf", "/v2/root", "/v1/memory.limit_in_bytes"}, func(path string) ([]byte, error) {
			switch path {
			case "/v2/leaf":
				return []byte("max"), nil
			case "/v2/root":
				return nil, fs.ErrNotExist
			default:
				return nil, boom
			}
		})
		assert.ErrorIs(t, err, boom)
	})

	// A file that reads but does not parse is the machine saying something the
	// probe cannot make sense of -- not the same as saying nothing applies.
	t.Run("a malformed limit file is a failure worth reporting", func(t *testing.T) {
		t.Parallel()
		_, err := readCgroupLimit([]string{"/v2/memory.max"}, fakeFS(map[string]string{
			"/v2/memory.max": "not-a-number",
		}))
		require.Error(t, err)
		assert.NotErrorIs(t, err, errNoLimit)
	})
}
