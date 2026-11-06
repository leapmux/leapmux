//go:build windows

package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/util/useronly"
)

// runtimeDir returns the account-owned directory for runtime scratch
// state. Mode bits alone do not restrict inherited access control lists
// on Windows, so the directory is created with the account-only SDDL
// and its ownership verified.
func runtimeDir() (string, error) {
	if fromEnv := os.Getenv("LEAPMUX_RUNTIME_DIR"); fromEnv != "" {
		return filepath.Clean(fromEnv), nil
	}
	return filepath.Join(os.TempDir(), "leapmux"), nil
}

// controlURLFor derives the named-pipe control endpoint from the
// instance identity and the account. The account keeps two accounts'
// instances off one pipe name; the identity keeps two configurations of
// one account apart.
func controlURLFor(id InstanceID) string {
	return "npipe:leapmux-" + hashID(currentAccount()) + "-" + string(id)
}

// preparePrivateDir creates dir when missing with the account-only
// descriptor and verifies the present owner. A directory another
// account owns is a refusal before anything inside it is modified.
func preparePrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		sddl, sddlErr := useronly.DirectorySDDL()
		if sddlErr != nil {
			return fmt.Errorf("derive the private directory descriptor: %w", sddlErr)
		}
		name, ptrErr := windows.UTF16PtrFromString(dir)
		if ptrErr != nil {
			return ptrErr
		}
		sd, sdErr := windows.SecurityDescriptorFromString(sddl)
		if sdErr != nil {
			return sdErr
		}
		sa := &windows.SecurityAttributes{Length: uint32(len(sddl)), InheritHandle: 0, SecurityDescriptor: sd}
		if err := windows.CreateDirectory(name, sa); err != nil && !errorsAlreadyExists(err) {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		return verifyPrivateDir(dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("lifecycle: %s exists and is not a directory", dir)
	}
	return verifyPrivateDir(dir)
}

func errorsAlreadyExists(err error) bool {
	return err == windows.ERROR_ALREADY_EXISTS
}

// verifyPrivateDir re-checks a directory after creation or on reuse:
// the current account must own it.
func verifyPrivateDir(dir string) error {
	owned, err := useronly.OwnedByCurrentUser(dir)
	if err != nil {
		return fmt.Errorf("read the owner of %s: %w", dir, err)
	}
	if !owned {
		return fmt.Errorf("lifecycle: %s belongs to another account", dir)
	}
	return nil
}

// verifyPrivateFile rejects a record or token another account owns; the
// creating descriptor admits this account alone, so a foreign owner
// means the file was replaced.
func verifyPrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("lifecycle: %s is not a regular file", path)
	}
	return verifyPrivateDir(path)
}

// maxUnixSocketPath is unused on Windows; named pipes have no length
// limit of this shape.
const maxUnixSocketPath = 0
