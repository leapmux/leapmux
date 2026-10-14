package service

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/util/pathutil"
)

// WorktreeLifecycle serializes directory removal with live-tab admission.
// A tab takes read locks for its directory and ancestors before it writes its live row.
// Removal takes the worktree root's write lock through its final reference check and Git operation.
// The zero value is ready for use. A Service and its payload store must share one instance.
type WorktreeLifecycle struct {
	locks sync.Map
	// beforeAdmissionFn controls the point before lock acquisition in tests.
	beforeAdmissionFn func()
}

func (l *WorktreeLifecycle) directoryLock(dir string) *sync.RWMutex {
	key := filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	value, _ := l.locks.LoadOrStore(key, &sync.RWMutex{})
	return value.(*sync.RWMutex)
}

// admit captures existing directories before it waits, then validates them under the locks.
// A planned new worktree can be absent. Its live row protects the path before Git creates it.
func (l *WorktreeLifecycle) admit(dirs ...string) (func(), error) {
	type directory struct{ original, canonical string }
	var existing []directory
	keys := make(map[string]struct{})
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		canonical, err := canonicalGitDirectory(dir)
		if err != nil {
			continue
		}
		existing = append(existing, directory{dir, canonical})
		for ancestor := canonical; ; ancestor = filepath.Dir(ancestor) {
			key := ancestor
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			keys[key] = struct{}{}
			if filepath.Dir(ancestor) == ancestor {
				break
			}
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.Sort(ordered)
	if l.beforeAdmissionFn != nil {
		l.beforeAdmissionFn()
	}
	locks := make([]*sync.RWMutex, 0, len(ordered))
	for _, key := range ordered {
		lock := l.directoryLock(key)
		lock.RLock()
		locks = append(locks, lock)
	}
	release := func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].RUnlock()
		}
	}
	for _, dir := range existing {
		canonical, err := canonicalGitDirectory(dir.original)
		if err != nil || !pathutil.SamePath(canonical, dir.canonical) {
			release()
			return nil, fmt.Errorf("tab directory changed during admission: %s", dir.original)
		}
	}
	return release, nil
}

func (l *WorktreeLifecycle) remove(dir string) func() {
	lock := l.directoryLock(pathutil.CanonicalizeAbsent(dir))
	lock.Lock()
	return lock.Unlock
}
