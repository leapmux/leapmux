package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/filelock"
)

// ErrDirectoryInUse reports a data directory another holder locks. The
// acquiring startup releases only the leases it took itself; it never
// waits on a foreign holder while holding another lease.
var ErrDirectoryInUse = errors.New("the data directory is in use by another instance")

// DirectoryLease is a held, ownership-validated lock on one storage
// component directory. The data lock file is directory-scoped -- its
// path holds neither the mode nor the instance identity -- so every
// mode that resolves to the same canonical directory contends for the
// same file.
//
// A lease is acquired through AcquireDirectory and released through
// Close, which is idempotent and retains its first cleanup result. The
// owning runtime releases it exactly once, after its storage closes.
type DirectoryLease struct {
	paths Paths
	owner DirectoryOwner
	mode  Role
	file  *os.File
	// Combined roots retain both component leases until startup takes them.
	// closedMu protects transfer against concurrent root cleanup.
	components map[Role]*DirectoryLease

	closeOnce  sync.Once
	closeErr   error
	closedMu   sync.Mutex
	closedFlag bool
}

// Path returns the component's canonical path.
func (l *DirectoryLease) Path() string { return l.paths.DataDir }

// Mode returns the storage component the lease holds the directory for.
func (l *DirectoryLease) Mode() Role { return l.mode }

// Owner returns the containing instance the lease was validated
// against.
func (l *DirectoryLease) Owner() DirectoryOwner { return l.owner }

// Closed reports whether Close has begun; a closing lease cannot transfer work.
func (l *DirectoryLease) Closed() bool {
	if l == nil {
		return true
	}
	l.closedMu.Lock()
	defer l.closedMu.Unlock()
	return l.closedFlag
}

// TakeComponent transfers one retained lease from a combined root. A missing,
// previously transferred, or closing component returns nil.
func (l *DirectoryLease) TakeComponent(mode Role) *DirectoryLease {
	if l == nil {
		return nil
	}
	l.closedMu.Lock()
	defer l.closedMu.Unlock()
	if l.closedFlag {
		return nil
	}
	component := l.components[mode]
	delete(l.components, mode)
	return component
}

// Close releases the data lock. It is safe on a nil lease, idempotent,
// and keeps the result of the first call. The ownership record and the
// lock file itself stay: the record is the directory's identity, and a
// lock file another command may hold must not be deleted from under it.
func (l *DirectoryLease) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closedMu.Lock()
		components := l.components
		l.components = nil
		l.closedFlag = true
		l.closedMu.Unlock()
		var failures []error
		for _, mode := range []Role{RoleWorker, RoleHub} {
			failures = append(failures, components[mode].Close())
		}
		if l.file != nil {
			failures = append(failures, filelock.Unlock(l.file), l.file.Close())
		}
		l.closeErr = errors.Join(failures...)
	})
	return l.closeErr
}

// AcquireDirectory durably creates the component directory, takes its
// directory-scoped data lock without waiting, and validates its
// ownership record -- creating the record on first use.
//
// A solo/dev root also acquires both component leases before returning. This
// prevents runtime or service publication before detecting a component conflict.
// Startup takes those same leases through TakeComponent; Close releases any
// component that did not transfer.
//
// owner identifies the containing instance; componentMode identifies this storage
// component. A direct launcher passes itself as both.
//
// A conflicting owner, another account, or a held data lock is an
// error; the caller's other leases are the caller's to release. This
// function never waits on a foreign holder, so a startup that already
// holds one lease cannot deadlock behind a second.
func AcquireDirectory(owner DirectoryOwner, componentMode Role, dataDirectory string) (*DirectoryLease, error) {
	if _, modeErr := ParseRole(string(componentMode)); modeErr != nil {
		return nil, modeErr
	}
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return nil, err
	}
	if err := atomicfile.MkdirAllDurable(canonical); err != nil {
		return nil, fmt.Errorf("lifecycle: create the data directory %s: %w", canonical, err)
	}
	paths := pathsFor(canonical)
	if err := preparePrivateDir(paths.Dir); err != nil {
		return nil, err
	}

	if err := verifyExistingLifecycleResources(paths); err != nil {
		return nil, err
	}
	lock, err := filelock.Open(paths.DataLock, false)
	if err != nil {
		return nil, fmt.Errorf("lifecycle: open the data lock of %s: %w", canonical, err)
	}
	held, err := filelock.TryLock(lock)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lifecycle: lock %s: %w", paths.DataLock, err)
	}
	if !held {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %s", ErrDirectoryInUse, canonical)
	}

	lease := &DirectoryLease{paths: paths, owner: owner, mode: componentMode, file: lock}
	if err := ensureOwnershipRecord(paths, owner, componentMode, canonical); err != nil {
		_ = lease.Close()
		return nil, err
	}
	if componentMode == RoleSolo || componentMode == RoleDev {
		if err := lease.acquireComponents(); err != nil {
			return nil, errors.Join(err, lease.Close())
		}
	}
	return lease, nil
}

func (l *DirectoryLease) acquireComponents() error {
	modes := []Role{RoleHub, RoleWorker}
	paths := make([]string, len(modes))
	for index, mode := range modes {
		canonical, err := CanonicalDir(filepath.Join(l.Path(), mode.String()))
		if err != nil {
			return err
		}
		if canonical == l.Path() {
			return fmt.Errorf("the %s component resolves to the %s root %s; choose a separate data directory", mode, l.mode, canonical)
		}
		paths[index] = canonical
	}
	if paths[0] == paths[1] {
		return fmt.Errorf("the hub and worker components resolve to the same directory %s; choose separate directories", paths[0])
	}
	l.components = make(map[Role]*DirectoryLease, len(modes))
	for index, mode := range modes {
		component, err := AcquireDirectory(l.owner, mode, paths[index])
		if err != nil {
			return fmt.Errorf("acquire the %s component directory: %w", mode, err)
		}
		l.components[mode] = component
	}
	return nil
}

// ensureOwnershipRecord creates or verifies the private ownership
// record under the data lock, before any application state or
// credential write. A record that identifies any other instance is a
// conflict, even when that instance is stopped: it is not permission to
// overwrite the marker.
func ensureOwnershipRecord(paths Paths, owner DirectoryOwner, componentMode Role, canonical string) error {
	rec, err := readOwnershipRecord(paths.OwnershipRecord)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		rec = ownershipRecord{
			AccountID:     owner.AccountID,
			Mode:          componentMode,
			DataDirectory: canonical,
			OwnerMode:     owner.Mode,
			OwnerDataDir:  owner.Root,
		}
		if err := writeOwnershipRecord(paths.OwnershipRecord, rec); err != nil {
			return fmt.Errorf("write the ownership record of %s: %w", canonical, err)
		}
		return verifyPrivateFile(paths.OwnershipRecord)
	default:
		return fmt.Errorf("read the ownership record of %s: %w", canonical, err)
	}

	// The record exists: every field must agree with this acquisition.
	switch {
	case rec.AccountID != owner.AccountID,
		rec.Mode != componentMode,
		rec.DataDirectory != canonical,
		rec.OwnerMode != owner.Mode,
		rec.OwnerDataDir != owner.Root:
		return &OwnershipConflict{
			OwningMode: rec.OwnerMode,
			OwningRoot: rec.OwnerDataDir,
			Component:  rec.Mode,
			Path:       canonical,
		}
	}
	return nil
}

// VerifyOwnership reads a directory's ownership record without taking
// its data lock, for status and refusal paths that must not contend. A
// missing record reads as os.ErrNotExist.
func VerifyOwnership(dataDirectory string) (ownershipRecord, error) {
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return ownershipRecord{}, err
	}
	return readOwnershipRecord(pathsFor(canonical).OwnershipRecord)
}
