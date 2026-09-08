package pathutil

// Mirrors of the two GetDriveType results the drive filter checks for.
//
// Declared here rather than imported from golang.org/x/sys/windows so that
// drivesFromBitmask -- the bit math AND the filter rule -- compiles and runs
// on every host. roots_windows_test.go pins them against the real constants,
// which is what makes the tests on a non-Windows machine trustworthy.
const (
	driveUnknown   uint32 = 0 // DRIVE_UNKNOWN: the type is not determinable.
	driveNoRootDir uint32 = 1 // DRIVE_NO_ROOT_DIR: no volume is mounted here.
)

// maxDriveLetters is 26. GetLogicalDrives sets bit i for drive 'A'+i, and bits
// 26..31 are undefined. A loop over all 32 emits "[:\", "\:\" and four more
// roots that no filesystem has.
const maxDriveLetters = 26

// drivesFromBitmask turns a GetLogicalDrives mask into drive roots, in
// ascending letter order, and drops any letter whose driveType probe reports
// that no volume is mounted there.
//
// Split from the syscalls on purpose. The mask is a uint32 and the probe is a
// func, so a test on macOS or Linux can hand this function any machine's drive
// layout as one literal. What is left in roots_windows.go is two calls with no
// branching.
func drivesFromBitmask(mask uint32, driveType func(root string) uint32) []string {
	roots := make([]string, 0, maxDriveLetters)
	for i := 0; i < maxDriveLetters; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		// DRIVE_NO_ROOT_DIR is the one answer that means "the letter is in the
		// mask but nothing is mounted". DRIVE_UNKNOWN is KEPT: it says "I
		// cannot classify this", not "it is not there", and the mask already
		// asserted that it is. To hide a real drive from the picker is worse
		// than to list one whose ListDirectory then fails, which the tree
		// already renders as an error for any unreadable directory. An empty
		// optical drive and a mapped share report CDROM and REMOTE, so both
		// stay.
		if driveType(root) == driveNoRootDir {
			continue
		}
		roots = append(roots, root)
	}
	return roots
}
