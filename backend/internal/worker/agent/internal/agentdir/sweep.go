package agentdir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// The limits of the stale hooks.
const (
	// staleHookTimeout limits one OnStale call. The hook's context ends then.
	staleHookTimeout = 15 * time.Second
	// staleHookGrace is how long the sweep waits after staleHookTimeout for the
	// hooks to return. A hook can still act when its context ends: the Cline
	// hook kills the daemon that it verified.
	staleHookGrace = 5 * time.Second
)

// sweepParent removes the stale directories of parent and closes done. For
// each stale directory it keeps the lock, runs the hook of its spec, and then
// removes the directory. The hooks run at the same time, each with its own
// deadline.
//
// The sweep closes done when every hook returned, or staleHookGrace after the
// deadline of the hooks. A hook that runs longer than that no longer delays a
// new directory, and its own directory goes when it returns.
func (d *Dirs) sweepParent(ctx context.Context, parent string, done chan<- struct{}) {
	defer close(done)
	info, err := os.Lstat(parent)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		err = d.checkParent(parent, info)
	}
	if err != nil {
		slog.Warn("skip the sweep of the agent directories", "dir", parent, "error", err)
		return
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		slog.Warn("list the agent directories", "dir", parent, "error", err)
		return
	}
	var wg sync.WaitGroup
	for _, entry := range entries {
		spec, ok := d.specOf(entry)
		if !ok {
			continue
		}
		dir := filepath.Join(parent, entry.Name())
		lock, stale, err := claimLock(filepath.Join(dir, lockFileName))
		if err != nil {
			slog.Warn("read the lock of an agent directory", "dir", dir, "error", err)
			continue
		}
		if !stale {
			continue
		}
		wg.Go(func() { d.removeStale(ctx, dir, spec, lock) })
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	limit := d.clock.NewTimer(staleHookTimeout+staleHookGrace, "agentdir", "sweep")
	defer limit.Stop()
	select {
	case <-finished:
	case <-limit.C:
		slog.Warn("stop waiting for the stale agent directories whose hook did not return", "dir", parent)
	}
}

// removeStale ends what a stale directory records, and removes the directory.
// The caller holds the directory's lock, and removeStale releases it. A hook
// that fails keeps the directory, whose lock is then free, so the next sweep
// tries again.
func (d *Dirs) removeStale(ctx context.Context, dir string, spec Spec, lock *lockFile) {
	if spec.OnStale != nil {
		if err := d.runStaleHook(ctx, spec, dir); err != nil {
			slog.Warn("keep a stale agent directory, whose hook failed", "dir", dir, "error", err)
			if err := lock.release(); err != nil {
				slog.Warn("release the lock of a stale agent directory", "dir", dir, "error", err)
			}
			return
		}
	}
	if err := removeLocked(dir, lock); err != nil {
		slog.Warn("remove a stale agent directory", "dir", dir, "error", err)
		return
	}
	slog.Info("removed the agent directory of an ended worker", "dir", dir)
}

// runStaleHook runs the hook of spec for dir, with a context that ends at the
// hook's deadline. A panic in the hook becomes an error: a stale directory
// whose hook panics stays, and it must not end the worker at each start.
func (d *Dirs) runStaleHook(ctx context.Context, spec Spec, dir string) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := d.clock.AfterFunc(staleHookTimeout, cancel, "agentdir", "stale-hook")
	defer deadline.Stop()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("the stale hook of an agent directory panicked", "dir", dir, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("the stale hook panicked: %v", r)
		}
	}()
	return spec.OnStale(ctx, dir)
}
