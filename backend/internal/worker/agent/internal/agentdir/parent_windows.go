//go:build windows

package agentdir

import (
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/util/useronly"
)

// mkdirPrivate creates dir with an access list that admits the current user
// alone. The list protects itself from the entries of its parent, such as a
// shared %TEMP%, and each directory and file that dir holds takes it.
func mkdirPrivate(dir string) error {
	sddl, err := useronly.DirectorySDDL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	attrs := &windows.SecurityAttributes{SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(*attrs))
	if err := windows.CreateDirectory(name, attrs); err != nil {
		return &fs.PathError{Op: "mkdir", Path: dir, Err: err}
	}
	return nil
}

// ownedByCurrentUser reports whether the owner SID of the file at path is the
// user who runs this process (see useronly.OwnedByCurrentUser).
func ownedByCurrentUser(path string, _ fs.FileInfo) (bool, error) {
	return useronly.OwnedByCurrentUser(path)
}

// restrictParent leaves the access list of an existing parent as it is. Only
// mkdirPrivate creates a parent, always with the list that admits the owner
// alone, and only the owner can change that list. The owner check refuses a
// parent that another user created first.
func restrictParent(string, fs.FileInfo) error { return nil }
