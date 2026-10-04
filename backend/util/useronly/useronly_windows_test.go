//go:build windows

package useronly

import (
	"os/user"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// parseDescriptor parses sddl with the SDDL parser of Windows and checks the
// parts that every descriptor of this package states: the owner and the
// group are the current user, and the access list holds exactly one entry,
// which allows access to that user. It returns the descriptor and that entry.
func parseDescriptor(t *testing.T, sddl string) (*windows.SECURITY_DESCRIPTOR, *windows.ACCESS_ALLOWED_ACE) {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	assert.Contains(t, sddl, u.Uid)

	sd, err := windows.SecurityDescriptorFromString(sddl)
	require.NoError(t, err, "the SDDL must parse")
	owner, _, err := sd.Owner()
	require.NoError(t, err)
	assert.Equal(t, u.Uid, owner.String(), "the current user owns the object")
	group, _, err := sd.Group()
	require.NoError(t, err)
	assert.Equal(t, u.Uid, group.String())

	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl, "a nil access list would admit everyone")
	require.Equal(t, uint16(1), dacl.AceCount, "one entry, for the user alone")
	var ace *windows.ACCESS_ALLOWED_ACE
	require.NoError(t, windows.GetAce(dacl, 0, &ace))
	assert.Equal(t, uint8(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType)
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	assert.Equal(t, u.Uid, sid.String(), "the entry grants access to the current user")
	return sd, ace
}

// A malformed or empty descriptor would leave the pipe open to every user, so
// the test parses the SDDL with the parser of Windows. It does not read the
// descriptor of a live pipe: GetNamedSecurityInfo on a pipe path needs an
// instance that waits, and that races the accept loop of winio.
func TestPipeSDDLAdmitsTheCurrentUserAlone(t *testing.T) {
	sddl, err := PipeSDDL()
	require.NoError(t, err)
	sd, ace := parseDescriptor(t, sddl)
	assert.Equal(t, windows.ACCESS_MASK(windows.GENERIC_ALL), ace.Mask)
	assert.Zero(t, ace.Header.AceFlags, "a pipe holds nothing that could inherit the entry")
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.Zero(t, control&windows.SE_DACL_PROTECTED, "a pipe takes no entry from a parent")
}

func TestDirectorySDDLAdmitsTheCurrentUserAloneToTheWholeTree(t *testing.T) {
	sddl, err := DirectorySDDL()
	require.NoError(t, err)
	sd, ace := parseDescriptor(t, sddl)
	inherit := uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	assert.Equal(t, inherit, ace.Header.AceFlags&inherit, "each file and directory inside takes the entry")
	assert.Zero(t, ace.Header.AceFlags&windows.INHERIT_ONLY_ACE, "the entry applies to the directory itself too")
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED, "the directory takes no entry from a shared parent")
}

func TestCurrentUserSIDParses(t *testing.T) {
	sid, err := CurrentUserSID()
	require.NoError(t, err)
	parsed, err := windows.StringToSid(sid)
	require.NoError(t, err)
	assert.Equal(t, sid, parsed.String())
}

func TestOwnedByCurrentUserAcceptsADirectoryOfThisProcess(t *testing.T) {
	owned, err := OwnedByCurrentUser(t.TempDir())
	require.NoError(t, err)
	assert.True(t, owned, "a directory that this process created")
}

// TrustedInstaller owns the Windows directory, and no user token states it as
// a user or a default owner.
func TestOwnedByCurrentUserRefusesADirectoryOfTheSystem(t *testing.T) {
	windowsDir, err := windows.GetSystemWindowsDirectory()
	require.NoError(t, err)
	owned, err := OwnedByCurrentUser(windowsDir)
	require.NoError(t, err)
	assert.False(t, owned)
}

func TestOwnedByCurrentUserFailsForAMissingPath(t *testing.T) {
	_, err := OwnedByCurrentUser(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
