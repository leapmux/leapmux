package pathutil

import (
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/validate"
)

// driveFixed is DRIVE_FIXED, the answer for an ordinary hard disk. Used as the
// default probe result so a test that is not about the filter says so.
const driveFixed uint32 = 3

func maskOf(letters ...rune) uint32 {
	var mask uint32
	for _, c := range letters {
		mask |= 1 << uint(c-'A')
	}
	return mask
}

func alwaysFixed(string) uint32 { return driveFixed }

func TestDrivesFromBitmask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mask uint32
		want []string
	}{
		{"an empty mask yields no roots", 0, []string{}},
		{"a single bit becomes its letter", maskOf('C'), []string{`C:\`}},
		{
			"bits are emitted in letter order however the mask was written",
			maskOf('Z') | maskOf('C') | maskOf('D'),
			[]string{`C:\`, `D:\`, `Z:\`},
		},
		{
			// Bits 26..31 are undefined. An `i < 32` loop turns them into
			// "[:\" and "`:\", which are not roots of anything.
			"bits above Z are ignored",
			1<<26 | 1<<31 | maskOf('C'),
			[]string{`C:\`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, drivesFromBitmask(tt.mask, alwaysFixed))
		})
	}

	t.Run("all twenty-six letters", func(t *testing.T) {
		t.Parallel()
		got := drivesFromBitmask(0x03FFFFFF, alwaysFixed)
		require.Len(t, got, 26)
		assert.Equal(t, `A:\`, got[0])
		assert.Equal(t, `Z:\`, got[25])
	})

	t.Run("every root is an uppercase letter with a trailing backslash", func(t *testing.T) {
		t.Parallel()
		re := regexp.MustCompile(`^[A-Z]:\\$`)
		for _, root := range drivesFromBitmask(0x03FFFFFF, alwaysFixed) {
			assert.Regexp(t, re, root)
		}
	})
}

func TestDrivesFromBitmask_FiltersOnDriveType(t *testing.T) {
	t.Parallel()

	t.Run("a letter with no volume is dropped", func(t *testing.T) {
		t.Parallel()
		probe := func(root string) uint32 {
			if root == `D:\` {
				return driveNoRootDir
			}
			return driveFixed
		}
		assert.Equal(t, []string{`C:\`, `E:\`}, drivesFromBitmask(maskOf('C', 'D', 'E'), probe))
	})

	// DRIVE_UNKNOWN means "cannot classify", not "not present". Hiding a real
	// drive from the picker is worse than listing one whose ListDirectory then
	// fails. Changing the fail-open rule must edit this test.
	t.Run("an unclassifiable drive is kept", func(t *testing.T) {
		t.Parallel()
		probe := func(string) uint32 { return driveUnknown }
		assert.Equal(t, []string{`C:\`}, drivesFromBitmask(maskOf('C'), probe))
	})

	t.Run("removable cdrom and remote drives are kept", func(t *testing.T) {
		t.Parallel()
		// DRIVE_REMOVABLE, DRIVE_REMOTE, DRIVE_CDROM.
		types := map[string]uint32{`C:\`: 2, `D:\`: 4, `E:\`: 5}
		probe := func(root string) uint32 { return types[root] }
		assert.Equal(t, []string{`C:\`, `D:\`, `E:\`}, drivesFromBitmask(maskOf('C', 'D', 'E'), probe))
	})

	t.Run("the probe sees exactly the roots that are returned", func(t *testing.T) {
		t.Parallel()
		var seen []string
		probe := func(root string) uint32 {
			seen = append(seen, root)
			return driveFixed
		}
		got := drivesFromBitmask(maskOf('C', 'D'), probe)
		assert.Equal(t, []string{`C:\`, `D:\`}, seen)
		assert.Equal(t, seen, got)
	})
}

// A caller that sorts or truncates the result in place must not corrupt a
// later call. Guards against a future cached package-level slice.
func TestDrivesFromBitmask_ReturnsAFreshSlice(t *testing.T) {
	t.Parallel()
	first := drivesFromBitmask(maskOf('C', 'D'), alwaysFixed)
	first[0] = "clobbered"
	assert.Equal(t, []string{`C:\`, `D:\`}, drivesFromBitmask(maskOf('C', 'D'), alwaysFixed))
}

func TestFilesystemRoots(t *testing.T) {
	t.Parallel()
	roots := FilesystemRoots()

	// Both branches guarantee this. A picker handed an empty list has nowhere
	// to browse and no way to say why.
	require.NotEmpty(t, roots)

	if runtime.GOOS == "windows" {
		re := regexp.MustCompile(`^[A-Z]:\\$`)
		for _, root := range roots {
			assert.Regexp(t, re, root)
		}
	} else {
		assert.Equal(t, []string{"/"}, roots)
	}

	// The cross-package contract that stops a future spelling change -- "C:"
	// without the separator, a lowercase letter -- from silently breaking
	// browsing. A root this RPC reports must be a path ListDirectory accepts.
	t.Run("every root is a path SanitizePath accepts unchanged", func(t *testing.T) {
		t.Parallel()
		for _, root := range roots {
			got, err := validate.SanitizePath(root, "")
			require.NoError(t, err, "root %q", root)
			assert.Equal(t, root, got)
		}
	})
}
