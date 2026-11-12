package lifecycle

import (
	"errors"
	"os"

	"github.com/leapmux/leapmux/util/useronly"
)

func verifyPrivateDir(path string) error { return useronly.VerifyDirectory(path) }

func verifyPrivateFile(path string) error { return useronly.VerifyFile(path) }

func verifyExistingPrivateFile(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return verifyPrivateFile(path)
}

func verifyExistingLifecycleResources(paths Paths) error {
	for _, path := range []string{
		paths.OwnershipRecord, paths.DataLock, paths.RuntimeLock, paths.OperationLock,
		paths.RuntimeRecord, paths.ControlToken, paths.ServiceSpec,
	} {
		if err := verifyExistingPrivateFile(path); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(paths.Logs); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return verifyPrivateDir(paths.Logs)
}
