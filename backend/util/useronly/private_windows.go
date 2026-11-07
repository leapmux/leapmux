//go:build windows

package useronly

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateAttributes(directory bool) (*windows.SecurityAttributes, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sddl := fmt.Sprintf("O:%sG:%sD:P(A;%s;FA;;;%s)", sid, sid, inheritance, sid)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}, nil
}

func verifyHandle(handle windows.Handle, path string) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read private access for %s: %w", path, err)
	}
	owned, err := descriptorOwnedByCurrentUser(descriptor)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("%s belongs to another account", path)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return fmt.Errorf("%s has an unrestricted access list", path)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var entry *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &entry); err != nil {
			return err
		}
		switch entry.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			principal := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
			if principal.Equals(user.User.Sid) {
				continue
			}
		}
		return fmt.Errorf("%s grants access outside this account", path)
	}
	return nil
}

func openResource(path string, directory, appendOnly bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	} else if appendOnly {
		access |= windows.FILE_APPEND_DATA
	} else {
		access |= windows.GENERIC_READ
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private resource", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || !directory && !info.Mode().IsRegular()) {
		err = fmt.Errorf("%s is not the expected private file or directory", path)
	}
	if err == nil {
		err = verifyHandle(handle, path)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// OpenFile validates the opened file's native owner and access list.
func OpenFile(path string) (*os.File, error) { return openResource(path, false, false) }

// OpenAppend validates the opened file before appending any private bytes.
func OpenAppend(path string) (*os.File, error) { return openResource(path, false, true) }

// VerifyDirectory rejects links and access entries for another account.
func VerifyDirectory(path string) error {
	file, err := openResource(path, true, false)
	if err != nil {
		return err
	}
	return file.Close()
}

// Mkdir creates a private directory with protected access from its first instant.
func Mkdir(path string) error {
	attributes, err := privateAttributes(true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(name, attributes); err != nil {
		return &os.PathError{Op: "create private directory", Path: path, Err: err}
	}
	return nil
}

func temporaryPath(parent, pattern string) (string, error) {
	if strings.ContainsAny(pattern, `/\`) {
		return "", fmt.Errorf("temporary name must contain no path separator")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	suffix := hex.EncodeToString(random[:])
	if index := strings.LastIndex(pattern, "*"); index >= 0 {
		pattern = pattern[:index] + suffix + pattern[index+1:]
	} else {
		pattern += suffix
	}
	return filepath.Join(parent, pattern), nil
}

// MkdirTemp creates a uniquely owned private staging directory.
func MkdirTemp(parent, pattern string) (string, error) {
	for range 100 {
		path, err := temporaryPath(parent, pattern)
		if err != nil {
			return "", err
		}
		if err := Mkdir(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("cannot create a unique private staging directory")
}

// CreateTemp creates a temporary file with an explicit account-only descriptor.
func CreateTemp(parent, pattern string) (*os.File, error) {
	attributes, err := privateAttributes(false)
	if err != nil {
		return nil, err
	}
	for range 100 {
		path, err := temporaryPath(parent, pattern)
		if err != nil {
			return nil, err
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return nil, err
		}
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			return os.NewFile(uintptr(handle), path), nil
		}
		if !errors.Is(err, windows.ERROR_FILE_EXISTS) && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, &os.PathError{Op: "create private file", Path: path, Err: err}
		}
	}
	return nil, fmt.Errorf("cannot create a unique private temporary file")
}
