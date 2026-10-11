// Package agentdir keeps the private directory of each agent. A provider puts
// there what no other user may read: an auth token, a discovery record, a
// socket, or a copy of the user's settings or attached files.
//
// # Layout
//
// All agent directories of one user share one parent, and each agent has one
// directory in it:
//
//	<base>/leapmux-agents-<uid>/<prefix>-<pid>-<random>/
//
// <prefix> identifies the provider (Spec.Prefix). <pid> is the worker process
// that created the directory. It is there for a person who reads the listing,
// and no code decides anything from it. <random> makes the name unique. On
// Windows the parent is <base>\leapmux-agents, because Windows has no numeric
// user id. The owner check keeps other users out of it.
//
// The parent's name differs from the per-worker socket directories of
// controlipc, which use "leapmux-" and a prefix of the worker id under the same
// bases.
//
// # Bases
//
// The bases, the preferred one first, are:
//
//  1. $XDG_RUNTIME_DIR, which is private to the user and empty after each login.
//  2. The "run" directory under the worker's data directory.
//  3. os.TempDir(): $TMPDIR on Unix, %TEMP% on Windows.
//  4. /tmp, on Unix.
//
// New skips a base when the parent there is not a private directory of the
// user: another user owns it, it is a link, or it cannot be created. It logs a
// warning and tries the next base. It also skips a base where the path of the
// provider's socket (Spec.SocketName) does not fit the platform's limit.
//
// # Liveness and the sweep
//
// The worker holds an exclusive advisory lock on the lock file of each of its
// directories for the directory's whole life: flock on Unix, LockFileEx on
// Windows. The lock file is opened close-on-exec, and on Windows its handle is
// not inheritable, so no process that an agent starts keeps the lock. The kernel
// releases the lock when the worker ends, however it ends.
//
// A directory whose lock the sweep can take is stale: the worker that created
// it ended without removing it, and it left its secrets there. A pid cannot
// tell this, because the system gives a pid to a new process after the first
// one ends.
//
// Start sweeps each parent once, in the background. For each stale directory
// of a known provider, the sweep keeps the lock, runs the provider's OnStale
// with a deadline, and then removes the directory. New waits for the sweep of
// the parent that it chooses, so a new agent never starts beside what an ended
// worker left running.
package agentdir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/coder/quartz"
)

// Spec states the agent directories of one provider.
type Spec struct {
	// Prefix begins the name of each directory of the provider, for example
	// "cline". It holds 1 to 16 lowercase ASCII letters and digits, so the name
	// of a directory parses back into its parts.
	Prefix string
	// SocketName is the file name of the Unix domain socket that the provider
	// binds in each directory, or "" when it binds none. New then puts a
	// directory only under a base where the path of the socket fits the
	// platform's limit (MaxSocketPathBytes).
	SocketName string
	// OnStale, when set, ends what a stale directory of the provider records,
	// such as a process that outlived its worker. The sweep calls it while it
	// holds the directory's lock and before it removes the directory. Its
	// context ends at the deadline of the hook. An error keeps the directory, so
	// the sweep at the next worker start tries again.
	OnStale func(ctx context.Context, dir string) error
}

// prefixPattern is the form of Spec.Prefix.
var prefixPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Validate refuses a spec that the layout cannot hold.
func (s Spec) Validate() error {
	if !prefixPattern.MatchString(s.Prefix) {
		return fmt.Errorf("the agent directory prefix %q must be 1 to 16 lowercase letters and digits", s.Prefix)
	}
	if s.SocketName != "" && !isPlainFileName(s.SocketName) {
		return fmt.Errorf("the agent directory socket %q must be a plain file name", s.SocketName)
	}
	if s.SocketName == lockFileName {
		return fmt.Errorf("the agent directory socket %q is the name of the lock file", s.SocketName)
	}
	return nil
}

// isPlainFileName reports whether name identifies one entry of a directory on
// every platform.
func isPlainFileName(name string) bool {
	return name != "." && name != ".." && !strings.ContainsAny(name, `/\:`) && !strings.ContainsRune(name, 0)
}

// ValidateSpecs refuses specs that one set of directories cannot hold: a spec
// that Validate refuses, and two specs with one prefix.
func ValidateSpecs(specs []Spec) error {
	var errs []error
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[spec.Prefix] {
			errs = append(errs, fmt.Errorf("two agent directory specs have the prefix %q", spec.Prefix))
		}
		seen[spec.Prefix] = true
	}
	return errors.Join(errs...)
}

// Config states the agent directories of one worker.
type Config struct {
	// DataDir is the worker's data directory. The second base is the "run"
	// directory under it, which Start creates. "" leaves that base out.
	DataDir string
	// Specs states the directories of every provider that keeps them. The sweep
	// acts on the directories of these specs alone, and New refuses any other
	// spec.
	Specs []Spec
	// Bases replaces the default bases, the preferred one first. The worker
	// leaves it empty. A test sets a base of its own, so it never touches the
	// user's own directories.
	Bases []string
	// Clock drives the deadline of the stale hooks. nil selects the real clock.
	Clock quartz.Clock

	// ownedByUser replaces the check that the user owns a parent. The tests of
	// this package set it, because a test cannot create a directory of another
	// user.
	ownedByUser func(path string, info fs.FileInfo) (bool, error)
}

// Dirs is the set of agent directories of one worker: the bases that can hold
// them, and the sweep of the directories that ended workers left.
type Dirs struct {
	clock       quartz.Clock
	bases       []string
	specs       map[string]Spec
	ownedByUser func(path string, info fs.FileInfo) (bool, error)
	// swept holds, for the parent under each base, a channel that closes when
	// the sweep of that parent ends.
	swept map[string]chan struct{}
	// allSwept closes when every sweep ended.
	allSwept chan struct{}
}

// Start prepares the agent directories of one worker, and starts the sweep of
// each parent in the background. It returns at once. It fails only for specs
// that ValidateSpecs refuses. The sweep stops early when ctx ends.
func Start(ctx context.Context, cfg Config) (*Dirs, error) {
	if err := ValidateSpecs(cfg.Specs); err != nil {
		return nil, err
	}
	d := &Dirs{
		clock:       cfg.Clock,
		bases:       cfg.Bases,
		specs:       make(map[string]Spec, len(cfg.Specs)),
		ownedByUser: cfg.ownedByUser,
		swept:       make(map[string]chan struct{}),
		allSwept:    make(chan struct{}),
	}
	if d.clock == nil {
		d.clock = quartz.NewReal()
	}
	if len(d.bases) == 0 {
		d.bases = prepareBases(cfg.DataDir)
	}
	d.bases = usableBases(d.bases)
	if d.ownedByUser == nil {
		d.ownedByUser = ownedByCurrentUser
	}
	for _, spec := range cfg.Specs {
		d.specs[spec.Prefix] = spec
	}
	done := make([]chan struct{}, 0, len(d.bases))
	for _, base := range d.bases {
		parent := parentOf(base)
		ch := make(chan struct{})
		d.swept[parent] = ch
		done = append(done, ch)
		go d.sweepParent(ctx, parent, ch)
	}
	go func() {
		for _, ch := range done {
			<-ch
		}
		close(d.allSwept)
	}()
	return d, nil
}

// Swept returns a channel that closes when the sweep of every parent ended.
func (d *Dirs) Swept() <-chan struct{} {
	return d.allSwept
}

// runDirName is the base under the worker's data directory.
const runDirName = "run"

// prepareBases returns the default bases of a worker whose data directory is
// dataDir, the preferred one first. It creates the base under dataDir, and
// leaves that base out when it cannot.
func prepareBases(dataDir string) []string {
	var bases []string
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" && filepath.IsAbs(dir) {
		bases = append(bases, dir)
	}
	if dataDir != "" {
		run := filepath.Join(dataDir, runDirName)
		if err := os.MkdirAll(run, 0o700); err != nil {
			slog.Warn("leave out the agent directory base under the data directory", "dir", run, "error", err)
		} else if abs, err := filepath.Abs(run); err == nil {
			bases = append(bases, abs)
		}
	}
	bases = append(bases, os.TempDir())
	if runtime.GOOS != "windows" {
		bases = append(bases, "/tmp")
	}
	return bases
}

// usableBases drops each base that is not an absolute path, which would put
// the parent under the worker's working directory, and each base that
// identifies the same directory as an earlier one, such as $TMPDIR when it is
// /tmp. It keeps the order.
func usableBases(bases []string) []string {
	seen := make(map[string]bool, len(bases))
	unique := make([]string, 0, len(bases))
	for _, base := range bases {
		if !filepath.IsAbs(base) {
			slog.Warn("leave out an agent directory base that is not an absolute path", "base", base)
			continue
		}
		key := filepath.Clean(base)
		if resolved, err := filepath.EvalSymlinks(key); err == nil {
			key = resolved
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, filepath.Clean(base))
	}
	return unique
}

// parentOf returns the parent of the agent directories under base.
func parentOf(base string) string {
	return filepath.Join(base, parentName())
}

// parentName is the name of the parent: "leapmux-agents-" and the user id. On
// Windows, which has no numeric user id, it is "leapmux-agents".
func parentName() string {
	if uid := os.Getuid(); uid >= 0 {
		return "leapmux-agents-" + strconv.Itoa(uid)
	}
	return "leapmux-agents"
}

// namePattern returns the start of the name of each directory that this worker
// creates for spec: the prefix and the worker's pid. os.MkdirTemp appends the
// random part.
func namePattern(spec Spec) string {
	return spec.Prefix + "-" + strconv.Itoa(os.Getpid()) + "-"
}

// maxRandomSuffix is the longest random part that os.MkdirTemp appends to a
// pattern: a uint32 in decimal.
const maxRandomSuffix = "4294967295"

// specOf returns the spec of a directory entry of a parent, from its name
// `<prefix>-<pid>-<random>`. It answers false for an entry that no known spec
// created, which the sweep leaves alone.
func (d *Dirs) specOf(entry fs.DirEntry) (Spec, bool) {
	if !entry.IsDir() {
		return Spec{}, false
	}
	prefix, rest, found := strings.Cut(entry.Name(), "-")
	if !found {
		return Spec{}, false
	}
	spec, known := d.specs[prefix]
	if !known {
		return Spec{}, false
	}
	pid, random, found := strings.Cut(rest, "-")
	if !found || !isDecimal(pid) || !isDecimal(random) {
		return Spec{}, false
	}
	return spec, true
}

// isDecimal reports whether s is one or more ASCII digits.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// errNotPrepared refuses a directory when the worker prepared none.
var errNotPrepared = errors.New("the worker prepared no agent directories")

// New creates the private directory of one agent of spec, under the first base
// that can hold it, and takes the directory's lock. It waits for the sweep of
// the parent that it chooses, or until ctx ends. The caller removes the
// directory with Close.
func (d *Dirs) New(ctx context.Context, spec Spec) (*Dir, error) {
	if d == nil {
		return nil, errNotPrepared
	}
	registered, known := d.specs[spec.Prefix]
	if !known || registered.SocketName != spec.SocketName {
		return nil, fmt.Errorf("the worker sweeps no agent directories of the spec with the prefix %q; state that spec on the provider's registration", spec.Prefix)
	}
	var refusals []error
	for _, base := range d.bases {
		parent := parentOf(base)
		if spec.SocketName != "" {
			worst := filepath.Join(parent, namePattern(spec)+maxRandomSuffix, spec.SocketName)
			if limit := MaxSocketPathBytes(); len(worst) > limit {
				refusals = append(refusals, fmt.Errorf("%s: the socket path %s is %d bytes, and the limit is %d on this platform", base, worst, len(worst), limit))
				continue
			}
		}
		select {
		case <-d.swept[parent]:
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the sweep of %s: %w", parent, ctx.Err())
		}
		dir, err := d.create(parent, spec)
		if err != nil {
			slog.Warn("skip a base of the agent directories", "base", base, "error", err)
			refusals = append(refusals, fmt.Errorf("%s: %w", base, err))
			continue
		}
		return dir, nil
	}
	return nil, fmt.Errorf("no base can hold a private agent directory: %w", errors.Join(refusals...))
}

// createAttempts limits how often create makes a new directory after the sweep
// of another worker took the one before.
const createAttempts = 3

// errForeignParent refuses a parent that another user owns.
var errForeignParent = errors.New("another user owns the directory")

// create makes one directory of spec under parent, and takes its lock.
func (d *Dirs) create(parent string, spec Spec) (*Dir, error) {
	if err := d.ensureParent(parent); err != nil {
		return nil, err
	}
	for range createAttempts {
		path, err := os.MkdirTemp(parent, namePattern(spec))
		if err != nil {
			return nil, fmt.Errorf("create the agent directory: %w", err)
		}
		lock, err := createLock(filepath.Join(path, lockFileName))
		if errors.Is(err, errLockTaken) {
			// The sweep of another worker read the new directory as a stale one,
			// which it is until its lock exists, and that sweep removes it.
			continue
		}
		if err != nil {
			_ = os.RemoveAll(path)
			return nil, fmt.Errorf("lock the agent directory: %w", err)
		}
		return &Dir{path: path, lock: lock}, nil
	}
	return nil, fmt.Errorf("the sweep of another worker took each new agent directory in %s, %d times", parent, createAttempts)
}

// ensureParent creates parent as a private directory of the user, or checks
// the one that exists.
func (d *Dirs) ensureParent(parent string) error {
	if err := mkdirPrivate(parent); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	return d.checkParent(parent, info)
}

// checkParent refuses a parent that is not a plain directory of the user, such
// as a link or a directory of another user. It makes a directory of the user
// private when it is not (see restrictParent).
func (d *Dirs) checkParent(parent string, info fs.FileInfo) error {
	if info.Mode().Type() != fs.ModeDir {
		return fmt.Errorf("%s is not a directory", parent)
	}
	owned, err := d.ownedByUser(parent, info)
	if err != nil {
		return fmt.Errorf("read the owner of %s: %w", parent, err)
	}
	if !owned {
		return fmt.Errorf("%w: %s", errForeignParent, parent)
	}
	return restrictParent(parent, info)
}
