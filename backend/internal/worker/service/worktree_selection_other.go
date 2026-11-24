//go:build !darwin && !linux && !windows

package service

func worktreeDirectoryIdentity(string) (worktreeDirectoryID, error) {
	return worktreeDirectoryID{}, nil
}
