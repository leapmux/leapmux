//go:build windows

package useronly

import (
	"errors"
	"fmt"
	"os/user"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CurrentUserSID returns the security identifier (SID) of the user who runs
// this process, in its string form, such as "S-1-5-21-...-1001". On Windows
// os/user reads it from the token of the process.
func CurrentUserSID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("find the current user: %w", err)
	}
	return u.Uid, nil
}

// PipeSDDL returns the descriptor of a named pipe that the current user alone
// can open, in the Security Descriptor Definition Language (SDDL). Its one
// entry grants GENERIC_ALL to the user.
func PipeSDDL() (string, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("O:%sG:%sD:(A;;GA;;;%s)", sid, sid, sid), nil
}

// DirectorySDDL returns the descriptor of a directory that the current user
// alone can reach, in SDDL:
//
//   - The one entry grants FILE_ALL_ACCESS to the user. Its OI and CI flags
//     make each file and directory that the directory holds take the same
//     entry.
//   - The P flag protects the access list, so the directory takes no entry
//     from its own parent, such as a shared %TEMP% that grants access to other
//     users.
func DirectorySDDL() (string, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("O:%sG:%sD:P(A;OICI;FA;;;%s)", sid, sid, sid), nil
}

// OwnedByCurrentUser reports whether the owner of the file or directory at path
// is the user who runs this process, or the default owner that this process
// gives each object that it creates. The two differ for an elevated member of
// the Administrators group, whose new objects that group owns. The object of
// another user has neither owner, unless that user is an elevated
// administrator, who can reach the object anyway.
func OwnedByCurrentUser(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, fmt.Errorf("read the owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false, fmt.Errorf("read the owner of %s: %w", path, err)
	}
	if owner == nil {
		return false, nil
	}
	token := windows.GetCurrentProcessToken()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return false, fmt.Errorf("read the user of this process: %w", err)
	}
	if owner.Equals(tokenUser.User.Sid) {
		return true, nil
	}
	defaultOwner, err := tokenDefaultOwner(token)
	if err != nil {
		return false, err
	}
	return owner.Equals(defaultOwner), nil
}

// tokenOwner is the TOKEN_OWNER structure: the default owner of each object
// that a process with the token creates.
type tokenOwner struct {
	Owner *windows.SID
}

// tokenDefaultOwner returns the default owner that token gives each new
// object. x/sys/windows reads the user and the primary group of a token, but
// not this.
func tokenDefaultOwner(token windows.Token) (*windows.SID, error) {
	size := uint32(64)
	for {
		buf := make([]byte, size)
		err := windows.GetTokenInformation(token, windows.TokenOwner, &buf[0], uint32(len(buf)), &size)
		if err == nil {
			return (*tokenOwner)(unsafe.Pointer(&buf[0])).Owner.Copy()
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || size <= uint32(len(buf)) {
			return nil, fmt.Errorf("read the default owner of this process: %w", err)
		}
	}
}
