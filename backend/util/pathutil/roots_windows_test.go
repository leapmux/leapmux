//go:build windows

package pathutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

// The one assertion that makes the drive-filter tests trustworthy on a
// non-Windows machine: roots.go mirrors these two constants so its filter rule
// compiles everywhere, and this pins the mirrors against the real values.
func TestDriveTypeConstantsMirrorWin32(t *testing.T) {
	t.Parallel()
	assert.EqualValues(t, windows.DRIVE_UNKNOWN, driveUnknown)
	assert.EqualValues(t, windows.DRIVE_NO_ROOT_DIR, driveNoRootDir)
}

// No t.Parallel: this reads SystemDrive, and TestSystemDriveRoot below writes
// it. Go finishes a non-parallel test, cleanup included, before it resumes the
// parallel ones -- but relying on that ordering to keep two tests apart is a
// race waiting for a scheduling change. Sequential costs microseconds.
func TestFilesystemRoots_IncludesTheSystemDrive(t *testing.T) {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		t.Skip("SystemDrive is not set")
	}
	assert.Contains(t, FilesystemRoots(), drive+`\`)
}

// The last-resort answer must follow the drive Windows booted from. A
// Windows-To-Go install, or one relettered with bcdedit, does not boot from C,
// and a hardcoded "C:\" would send the picker to a drive that may not exist.
//
// No t.Parallel: t.Setenv and parallel subtests cannot be combined.
func TestSystemDriveRoot(t *testing.T) {
	t.Setenv("SystemDrive", "E:")
	assert.Equal(t, `E:\`, systemDriveRoot())

	t.Setenv("SystemDrive", "")
	assert.Equal(t, `C:\`, systemDriveRoot(), "an unset SystemDrive falls back to C")
}
