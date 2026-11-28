//go:build !darwin && !linux && !windows

package service

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	if _, err := verifyWorktreeDirectory(path); err != nil {
		return worktreeDirectoryID{}, err
	}
	return worktreeDirectoryID{}, nil
}
