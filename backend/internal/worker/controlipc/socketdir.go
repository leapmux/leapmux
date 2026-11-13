package controlipc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/canonicalpath"
	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/useronly"
)

// SweepSocketDir removes leftover control sockets. Callers hold the Worker data-
// directory lease and, for a fallback namespace, its namespace lock. Regular
// files and symlinks remain untouched.
func SweepSocketDir(dir string) error {
	if err := atomicfile.MkdirAllPrivate(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sock") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect control socket %s: %w", path, err)
		}
		if info.Mode()&os.ModeSocket != 0 {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove control socket %s: %w", path, err)
			}
		}
	}
	return nil
}

const (
	socketNamespaceLockName     = ".namespace.lock"
	socketNamespaceIdentityName = ".namespace-directory"
)

// SocketDirectory owns a selected directory and its optional namespace lock.
// Its caller holds the Worker's data-directory lease until Close returns.
// Paths are fixed at preparation, independently of registration or Worker ID.
type SocketDirectory struct {
	path     string
	lock     *os.File
	mu       sync.Mutex
	closed   bool
	closeErr error
}

// Path returns the selected absolute directory. Windows pipe names ignore it.
func (d *SocketDirectory) Path() string { return d.path }

// PrepareSocketDirectory selects and validates the directory before any sweep
// or listener creation. Its caller already holds the Worker data-directory lease.
// Only an excessive pathname length selects the fixed private fallback.
func PrepareSocketDirectory(dataDir string) (*SocketDirectory, error) {
	return prepareSocketDirectory(dataDir, "/tmp")
}

func prepareSocketDirectory(dataDir, fallbackRoot string) (*SocketDirectory, error) {
	canonical, err := lifecycle.CanonicalDir(dataDir)
	if err != nil {
		return nil, err
	}
	preferred := SocketDir(canonical)
	if runtime.GOOS == "windows" {
		return &SocketDirectory{path: preferred}, nil
	}
	// An existing refused directory must not become an excuse to use another
	// location, even when its pathname would also exceed the socket limit.
	if _, err := os.Lstat(preferred); err == nil {
		if err := useronly.VerifyDirectory(preferred); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if socketPathFits(preferred) {
		if err := atomicfile.MkdirAllPrivate(preferred); err != nil {
			return nil, err
		}
		return &SocketDirectory{path: preferred}, nil
	}
	root, err := canonicalpath.Directory(fallbackRoot)
	if err != nil {
		return nil, err
	}
	accountID := lifecycle.CurrentAccountID()
	account := filepath.Join(root, "leapmux-"+accountID)
	instance := lifecycle.NewInstanceID(accountID, lifecycle.RoleWorker, canonical)
	namespace := filepath.Join(account, "c-"+instance.String())
	if !socketPathFits(namespace) {
		return nil, fmt.Errorf("%w: control socket directory %q cannot fit the longest socket filename", locallisten.ErrSocketPathTooLong, namespace)
	}
	if err := atomicfile.MkdirAllPrivate(account); err != nil {
		return nil, err
	}
	if err := atomicfile.MkdirAllPrivate(namespace); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(namespace, socketNamespaceLockName)
	if _, err := os.Lstat(lockPath); err == nil {
		if err := useronly.VerifyFile(lockPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lock, err := filelock.Open(lockPath, false)
	if err != nil {
		return nil, err
	}
	held, err := filelock.TryLock(lock)
	if err != nil || !held {
		if err == nil {
			err = fmt.Errorf("control socket namespace %s is already in use", namespace)
		}
		return nil, errors.Join(err, lock.Close())
	}
	directory := &SocketDirectory{path: namespace, lock: lock}
	refuse := func(err error) (*SocketDirectory, error) { return nil, errors.Join(err, directory.Close()) }
	if err := useronly.VerifyFile(lockPath); err != nil {
		return refuse(err)
	}
	marker := filepath.Join(namespace, socketNamespaceIdentityName)
	identity, err := useronly.ReadFile(marker)
	switch {
	case err == nil:
		if string(identity) != canonical {
			return refuse(fmt.Errorf("control socket namespace %s belongs to another data directory", namespace))
		}
	case errors.Is(err, os.ErrNotExist):
		entries, readErr := os.ReadDir(namespace)
		if readErr != nil {
			return refuse(readErr)
		}
		for _, entry := range entries {
			// MkdirAllPrivate already validates its durable-publication receipt.
			// Every other existing object would be an unclaimed namespace's data.
			if entry.Name() != socketNamespaceLockName && entry.Name() != atomicfile.DirectoryPublicationRecordName {
				return refuse(fmt.Errorf("unclaimed control socket namespace %s contains %s", namespace, entry.Name()))
			}
		}
		if err := atomicfile.WriteFileDurable(marker, []byte(canonical), 0o600); err != nil {
			return refuse(err)
		}
	default:
		return refuse(err)
	}
	return directory, nil
}

func socketPathFits(dir string) bool {
	path := strings.TrimPrefix(SocketURL(dir, "", SocketKindAgent, "12345678"), "unix:")
	return len(path) <= locallisten.MaxUnixSocketPathBytes()
}

// Sweep removes orphan sockets only while this directory owner remains live.
// The Worker's data-directory lease also remains held by the caller.
func (d *SocketDirectory) Sweep() error {
	if d == nil {
		return errors.New("control socket directory is unavailable")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("control socket directory is closed")
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return SweepSocketDir(d.path)
}

// Close releases the namespace lock. The factory calls it after every listener
// closes. The directory, identity marker and lock file remain for a later run.
func (d *SocketDirectory) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return d.closeErr
	}
	d.closed = true
	if d.lock != nil {
		d.closeErr = errors.Join(filelock.Unlock(d.lock), d.lock.Close())
	}
	return d.closeErr
}
