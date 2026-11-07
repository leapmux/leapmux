// Package lifecycle owns the process lifecycle LeapMux's CLI modes share:
// one instance per account, mode, and canonical data directory; the
// directory-scoped locks that make that true; the private local control
// interface a foreground or background instance answers; and the status
// and diagnostics every lifecycle command reports.
package lifecycle

import (
	"crypto/sha256"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/canonicalpath"
)

// Role is one of LeapMux's four CLI modes. The same vocabulary identifies a
// combined instance's containing mode (solo, dev) and a component's
// storage mode (hub, worker): a directory's ownership record states
// both, which is what keeps a direct Hub and a combined instance's Hub
// component from sharing a storage directory.
type Role string

const (
	RoleHub    Role = "hub"
	RoleWorker Role = "worker"
	RoleSolo   Role = "solo"
	RoleDev    Role = "dev"
)

// ParseRole validates one mode name.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleHub, RoleWorker, RoleSolo, RoleDev:
		return Role(s), nil
	default:
		return "", fmt.Errorf("lifecycle: unknown mode %q", s)
	}
}

// Combined reports whether the role identifies a mode that contains
// components rather than being one.
func (r Role) Combined() bool { return r == RoleSolo || r == RoleDev }

// Component reports whether the role identifies a storage component a
// combined instance or a direct launcher can own.
func (r Role) Component() bool { return r == RoleHub || r == RoleWorker }

// String spells the role.
func (r Role) String() string { return string(r) }

// InstanceID identifies one instance: the account, the mode, and the
// canonical data directory, hashed into one stable short token. It
// identifies the native service definition, the control endpoint, and the
// log attempt prefix, so two configurations that resolve to the same
// account, mode, and directory share one instance identity.
type InstanceID string

// hashID derives a short hex identity from its parts.
func hashID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// NewInstanceID derives the instance identity for one account, mode,
// and canonical data directory.
func NewInstanceID(accountID string, role Role, canonicalDir string) InstanceID {
	return InstanceID(hashID(accountID, string(role), canonicalDir))
}

// String spells the identity.
func (id InstanceID) String() string { return string(id) }

// CurrentAccountID reports the current account's identifier: the numeric
// uid on Unix, the account SID on Windows. An identity a directory's
// ownership record pins must be one the operating system spells the
// same way in a later command.
func CurrentAccountID() string {
	return currentAccount()
}

// CanonicalDir resolves path to the form identity derives from: an
// absolute path with every existing symlink resolved. A path that does
// not exist yet canonicalizes as far as it can -- its existing prefix
// resolved, the rest cleaned -- so the same future directory yields the
// same identity before and after it is created.
func CanonicalDir(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("lifecycle: a data directory is required")
	}
	return canonicalpath.Directory(path)
}

// DirectoryOwner states the instance that owns a storage component: the
// account, the containing mode, and the canonical containing root. A
// direct Hub or Worker owns itself; a combined instance (solo, dev)
// owns both of its components under one root.
type DirectoryOwner struct {
	// AccountID is the operating-system account that runs the instance.
	AccountID string
	// Mode is the containing instance's mode: itself for a direct Hub
	// or Worker, solo or dev for a combined one.
	Mode Role
	// Root is the canonical containing data root. For a direct launcher
	// it is the component's own directory.
	Root string
}

// NewDirectoryOwner builds the owner of a direct instance's own
// directory.
func NewDirectoryOwner(role Role, canonicalDir string) DirectoryOwner {
	return DirectoryOwner{AccountID: CurrentAccountID(), Mode: role, Root: canonicalDir}
}
