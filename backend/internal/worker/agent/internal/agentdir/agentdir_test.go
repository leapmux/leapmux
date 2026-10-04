package agentdir

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// testSpec is the spec of the tests that need no socket and no hook.
var testSpec = Spec{Prefix: "test"}

// newBase creates a base for the agent directories of one test.
func newBase(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// startDirs starts the directories that cfg states and waits for the sweep.
func startDirs(t *testing.T, cfg Config) *Dirs {
	t.Helper()
	dirs, err := Start(context.Background(), cfg)
	require.NoError(t, err)
	waitSwept(t, dirs)
	return dirs
}

// waitSwept waits until the sweep of every parent of dirs ended.
func waitSwept(t *testing.T, dirs *Dirs) {
	t.Helper()
	select {
	case <-dirs.Swept():
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the sweep did not end")
	}
}

// newDir creates one directory of spec and closes it when the test ends.
func newDir(t *testing.T, dirs *Dirs, spec Spec) *Dir {
	t.Helper()
	dir, err := dirs.New(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}

// staleSeq makes the random part of each stale directory of the tests unique.
var staleSeq atomic.Int64

// staleDir makes a directory of spec under base as a worker with pid leaves it
// when it ends: nothing holds its lock, and it still holds a secret.
// withLockFile states whether the lock file exists; a worker that ended while
// it created the directory left none.
func staleDir(t *testing.T, base string, spec Spec, pid int, withLockFile bool) string {
	t.Helper()
	parent := parentOf(base)
	require.NoError(t, os.MkdirAll(parent, 0o700))
	dir := filepath.Join(parent, fmt.Sprintf("%s-%d-%d", spec.Prefix, pid, 100+staleSeq.Add(1)))
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"token":"t"}`), 0o600))
	if withLockFile {
		require.NoError(t, os.WriteFile(filepath.Join(dir, lockFileName), nil, 0o600))
	}
	return dir
}

// The environment of the helper process of these tests, which is this test
// binary.
const (
	// helperModeEnv selects what the helper does: "hold" takes the lock at
	// helperLockEnv, and "wait" does nothing. Either one then waits until its
	// stdin closes.
	helperModeEnv = "LEAPMUX_TEST_AGENTDIR_HELPER"
	helperLockEnv = "LEAPMUX_TEST_AGENTDIR_LOCK"
)

// TestHelperProcessAgentDir is the helper process. It does nothing in the test
// process itself.
func TestHelperProcessAgentDir(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	if mode == "hold" {
		lock, held, err := claimLock(os.Getenv(helperLockEnv))
		if err != nil || !held {
			fmt.Printf("failed: held=%v error=%v\n", held, err)
			os.Exit(1)
		}
		defer func() { _ = lock.release() }()
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// helper is a running helper process.
type helper struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// startHelper starts the helper process in mode and waits until it is ready.
// The end of the test ends it.
func startHelper(t *testing.T, mode, lockPath string) *helper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessAgentDir$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode, helperLockEnv+"="+lockPath)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	h := &helper{cmd: cmd, stdin: stdin}
	t.Cleanup(h.stop)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(line))
	return h
}

// stop closes the helper's stdin, which ends it, and waits for it.
func (h *helper) stop() {
	_ = h.stdin.Close()
	_ = h.cmd.Wait()
}

func TestNewCreatesALockedPrivateDirectoryUnderTheUsersParent(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	dir := newDir(t, dirs, testSpec)

	assert.Equal(t, parentOf(base), filepath.Dir(dir.Path()))
	assert.Regexp(t, regexp.MustCompile(`^test-`+strconv.Itoa(os.Getpid())+`-\d+$`), filepath.Base(dir.Path()),
		"the name carries the prefix, the worker's pid and a random part")
	info, err := os.Stat(dir.Path())
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	if runtime.GOOS != "windows" {
		assert.Equal(t, "leapmux-agents-"+strconv.Itoa(os.Getuid()), filepath.Base(parentOf(base)))
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		parent, err := os.Stat(parentOf(base))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), parent.Mode().Perm())
	} else {
		assert.Equal(t, "leapmux-agents", filepath.Base(parentOf(base)))
	}

	_, held, err := claimLock(filepath.Join(dir.Path(), lockFileName))
	require.NoError(t, err)
	assert.False(t, held, "the worker holds the lock of its directory")
}

func TestNewGivesEachAgentItsOwnDirectory(t *testing.T) {
	t.Parallel()
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{newBase(t)}})
	first := newDir(t, dirs, testSpec)
	second := newDir(t, dirs, testSpec)
	assert.NotEqual(t, first.Path(), second.Path())
	assert.DirExists(t, first.Path())
	assert.DirExists(t, second.Path())
}

func TestNewRefusesASpecThatTheWorkerDoesNotSweep(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	dirs := startDirs(t, Config{Specs: []Spec{{Prefix: "sock", SocketName: "a.sock"}}, Bases: []string{base}})
	_, err := dirs.New(context.Background(), Spec{Prefix: "other"})
	require.ErrorContains(t, err, `prefix "other"`)
	_, err = dirs.New(context.Background(), Spec{Prefix: "sock", SocketName: "b.sock"})
	require.ErrorContains(t, err, `prefix "sock"`, "the socket must match the spec that the worker sweeps")
	assert.NoDirExists(t, parentOf(base), "a refused spec creates nothing")
}

func TestNewOfDirsThatTheWorkerDidNotPrepareFails(t *testing.T) {
	t.Parallel()
	var dirs *Dirs
	_, err := dirs.New(context.Background(), testSpec)
	require.ErrorIs(t, err, errNotPrepared)
}

func TestStartRefusesSpecsThatTheLayoutCannotHold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		specs []Spec
		want  string
	}{
		{"an empty prefix", []Spec{{}}, "prefix"},
		{"a capital letter", []Spec{{Prefix: "Cline"}}, "prefix"},
		{"a dash, which the name uses to part its fields", []Spec{{Prefix: "a-b"}}, "prefix"},
		{"a long prefix", []Spec{{Prefix: strings.Repeat("a", 17)}}, "prefix"},
		{"a socket in a subdirectory", []Spec{{Prefix: "a", SocketName: "x/y.sock"}}, "socket"},
		{"a socket with a backslash", []Spec{{Prefix: "a", SocketName: `x\y.sock`}}, "socket"},
		{"a socket named dot-dot", []Spec{{Prefix: "a", SocketName: ".."}}, "socket"},
		{"a socket named dot", []Spec{{Prefix: "a", SocketName: "."}}, "socket"},
		{"a socket with a colon, which Windows reads as a stream", []Spec{{Prefix: "a", SocketName: "x:y.sock"}}, "socket"},
		{"a socket with a NUL", []Spec{{Prefix: "a", SocketName: "x\x00y.sock"}}, "socket"},
		{"a socket named as the lock file", []Spec{{Prefix: "a", SocketName: lockFileName}}, "lock file"},
		{"two specs with one prefix", []Spec{{Prefix: "a"}, {Prefix: "a"}}, "two agent directory specs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dirs, err := Start(context.Background(), Config{Specs: tc.specs, Bases: []string{newBase(t)}})
			require.ErrorContains(t, err, tc.want)
			assert.Nil(t, dirs)
		})
	}
	require.NoError(t, ValidateSpecs([]Spec{
		{Prefix: "cline"}, {Prefix: "amp", SocketName: "bridge.sock"}, {Prefix: "a1"},
		{Prefix: strings.Repeat("a", 16)},
	}), "a prefix of 16 characters is the longest that the layout holds")
	require.NoError(t, ValidateSpecs(nil), "a worker with no provider that keeps a directory states no spec")
}

// ValidateSpecs reports every refusal at once, so a wiring mistake in two
// providers takes one run to find, not two.
func TestValidateSpecsReportsEveryRefusal(t *testing.T) {
	t.Parallel()
	err := ValidateSpecs([]Spec{{Prefix: "Bad"}, {Prefix: "ok"}, {Prefix: "ok"}, {Prefix: "x", SocketName: "a/b"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `prefix "Bad"`)
	assert.Contains(t, err.Error(), `two agent directory specs have the prefix "ok"`)
	assert.Contains(t, err.Error(), `socket "a/b"`)
}

// A worker that ended left its directories. Nothing holds their locks, so the
// sweep removes them, whatever pid their names carry. A pid in a name can
// belong to a new process by now.
func TestTheSweepRemovesEachDirectoryWhoseLockIsFree(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	reusedPID := staleDir(t, base, testSpec, os.Getppid(), true)
	noLockFile := staleDir(t, base, testSpec, os.Getppid(), false)
	ownPID := staleDir(t, base, testSpec, os.Getpid(), true)
	other := Spec{Prefix: "other"}
	unknown := staleDir(t, base, other, 1, true)
	notAgentDir := filepath.Join(parentOf(base), "notes")
	require.NoError(t, os.Mkdir(notAgentDir, 0o700))
	badName := filepath.Join(parentOf(base), "test-x-1")
	require.NoError(t, os.Mkdir(badName, 0o700))
	file := filepath.Join(parentOf(base), "test-1-2")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	assert.NoDirExists(t, reusedPID, "the pid of the name runs another process, and the lock is free")
	assert.NoDirExists(t, noLockFile, "a worker that ended before it created the lock file left the directory")
	assert.NoDirExists(t, ownPID, "an earlier process with this pid left the directory")
	assert.DirExists(t, unknown, "the sweep acts on the directories of known specs alone")
	assert.DirExists(t, notAgentDir)
	assert.DirExists(t, badName)
	assert.FileExists(t, file)
}

// A directory whose lock another process holds belongs to a worker that runs.
// Once that process ends, the next sweep removes the directory.
func TestTheSweepKeepsADirectoryWhoseLockAnotherProcessHolds(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	live := staleDir(t, base, testSpec, 1, true)
	holder := startHelper(t, "hold", filepath.Join(live, lockFileName))

	startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	assert.DirExists(t, live, "the process that holds the lock runs")
	assert.FileExists(t, filepath.Join(live, "secret.json"))

	holder.stop()
	require.True(t, holder.cmd.ProcessState.Success(), "the helper took the lock and ended by itself")
	startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	assert.NoDirExists(t, live, "the lock is free once its holder ended")
}

// The lock file is close-on-exec, and on Windows its handle is not
// inheritable. A process that the worker starts, such as an agent's daemon,
// therefore keeps no copy of the lock, and a directory is stale once its
// worker ended although that process still runs.
func TestAChildProcessKeepsNoCopyOfTheLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	lock, err := createLock(path)
	require.NoError(t, err)
	startHelper(t, "wait", "")
	require.NoError(t, lock.release())

	claimed, held, err := claimLock(path)
	require.NoError(t, err)
	assert.True(t, held, "the child that still runs holds no lock")
	require.NoError(t, claimed.release())
}

// The sweep runs the hook while the directory still holds what the hook
// reads, and only for a stale directory. New waits for the sweep of its
// parent, so it creates nothing while the hook runs, and it finds the stale
// directory gone.
func TestNewWaitsForTheSweepOfItsParent(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	stale := staleDir(t, base, testSpec, 1, true)
	live := staleDir(t, base, testSpec, 1, true)
	startHelper(t, "hold", filepath.Join(live, lockFileName))

	entered := make(chan string, 2)
	release := make(chan struct{})
	spec := testSpec
	spec.OnStale = func(_ context.Context, dir string) error {
		assert.FileExists(t, filepath.Join(dir, "secret.json"), "the hook runs before the removal")
		entered <- dir
		<-release
		return nil
	}
	dirs, err := Start(context.Background(), Config{Specs: []Spec{spec}, Bases: []string{base}})
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	select {
	case dir := <-entered:
		assert.Equal(t, stale, dir, "the hook runs for the stale directory alone")
	case <-ctx.Done():
		t.Fatal("the hook did not run")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = dirs.New(cancelled, spec)
	require.ErrorIs(t, err, context.Canceled, "New waits for the sweep, which still runs")
	entries, err := os.ReadDir(parentOf(base))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "New created nothing while the sweep ran")
	assert.DirExists(t, stale, "the removal waits for the hook")

	close(release)
	dir, err := dirs.New(ctx, spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	assert.NoDirExists(t, stale, "the sweep removed the stale directory before New returned")
	assert.DirExists(t, live)
	assert.Empty(t, entered, "the hook ran once")
}

// The hook gets a context that ends at its deadline. A hook that returns then
// fails, so its directory stays for the next sweep. A hook that ignores its
// context holds the sweep no longer than the grace after the deadline, and its
// directory goes only when it returns.
func TestTheStaleHookDeadlineHolds(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	obedient := staleDir(t, base, Spec{Prefix: "obedient"}, 1, true)
	stubborn := staleDir(t, base, Spec{Prefix: "stubborn"}, 1, true)
	obedientDone := make(chan error, 1)
	release := make(chan struct{})
	stubbornDone := make(chan struct{})
	specs := []Spec{
		{Prefix: "obedient", OnStale: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			obedientDone <- ctx.Err()
			return ctx.Err()
		}},
		{Prefix: "stubborn", OnStale: func(context.Context, string) error {
			<-release
			close(stubbornDone)
			return nil
		}},
	}
	clock := testutil.NewQuartzMock(t)
	hookTrap := clock.Trap().AfterFunc("agentdir", "stale-hook")
	defer hookTrap.Close()
	sweepTrap := clock.Trap().NewTimer("agentdir", "sweep")
	defer sweepTrap.Close()

	dirs, err := Start(context.Background(), Config{Specs: specs, Bases: []string{base}, Clock: clock})
	require.NoError(t, err)
	ctx := testutil.DeadlineContext(t)
	for range 2 {
		assert.Equal(t, staleHookTimeout, testutil.WaitForTimer(t, ctx, hookTrap))
	}
	assert.Equal(t, staleHookTimeout+staleHookGrace, testutil.WaitForTimer(t, ctx, sweepTrap))

	clock.Advance(staleHookTimeout).MustWait(ctx)
	select {
	case err := <-obedientDone:
		require.ErrorIs(t, err, context.Canceled, "the deadline ends the hook's context")
	case <-ctx.Done():
		t.Fatal("the deadline did not end the hook's context")
	}
	select {
	case <-dirs.Swept():
		t.Fatal("the sweep stopped waiting before the grace passed")
	default:
	}

	clock.Advance(staleHookGrace).MustWait(ctx)
	waitSwept(t, dirs)
	assert.DirExists(t, obedient, "a hook that failed keeps its directory")
	assert.DirExists(t, stubborn, "the directory stays until its hook returns")
	// The sweep stopped waiting at the grace, so the release of the kept
	// directory's lock can land a moment later.
	require.Eventually(t, func() bool {
		claimed, held, err := claimLock(filepath.Join(obedient, lockFileName))
		if err != nil || !held {
			return false
		}
		return claimed.release() == nil
	}, eventuallyLimit, eventuallyTick, "the sweep released the lock of the directory that it kept")

	close(release)
	<-stubbornDone
	require.Eventually(t, func() bool {
		_, err := os.Stat(stubborn)
		return errors.Is(err, fs.ErrNotExist)
	}, eventuallyLimit, eventuallyTick, "the directory goes once its hook returns")
}

// The limits of the waits for work that the sweep does after it stopped
// waiting. The limit is generous, because no passing test reaches it.
const (
	eventuallyLimit = 30 * time.Second
	eventuallyTick  = 10 * time.Millisecond
)

func TestTheSweepKeepsTheDirectoryOfAHookThatFails(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	failed := staleDir(t, base, testSpec, 1, true)
	spec := testSpec
	spec.OnStale = func(context.Context, string) error { return errors.New("the daemon still runs") }
	startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{base}})
	assert.DirExists(t, failed)
	assert.FileExists(t, filepath.Join(failed, "secret.json"))
	claimed, held, err := claimLock(filepath.Join(failed, lockFileName))
	require.NoError(t, err)
	assert.True(t, held, "the next sweep can take the directory again")
	require.NoError(t, claimed.release())
}

// A panic in a hook must not end the worker, or the worker would end at each
// start while the stale directory stays.
func TestTheSweepSurvivesAHookThatPanics(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	panicked := staleDir(t, base, testSpec, 1, true)
	spec := testSpec
	spec.OnStale = func(context.Context, string) error { panic("boom") }
	startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{base}})
	assert.DirExists(t, panicked, "a hook that panicked keeps its directory")
}

// The hooks of the stale directories run at the same time, so the sweep takes
// about one deadline, not one deadline for each directory.
func TestTheSweepRunsTheHooksAtTheSameTime(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	first := staleDir(t, base, testSpec, 1, true)
	second := staleDir(t, base, testSpec, 1, true)
	var running sync.WaitGroup
	running.Add(2)
	spec := testSpec
	spec.OnStale = func(context.Context, string) error {
		running.Done()
		running.Wait()
		return nil
	}
	startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{base}})
	assert.NoDirExists(t, first)
	assert.NoDirExists(t, second)
}

// C-L14: another user can create the parent first on a shared base. New then
// takes the next base, and the sweep leaves that parent alone.
func TestNewTakesTheNextBaseWhenAnotherUserOwnsTheParent(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	foreign, own := newBase(t), newBase(t)
	theirs := staleDir(t, foreign, testSpec, 1, true)
	dirs := startDirs(t, Config{
		Specs: []Spec{testSpec},
		Bases: []string{foreign, own},
		ownedByUser: func(path string, info fs.FileInfo) (bool, error) {
			if path == parentOf(foreign) {
				return false, nil
			}
			return ownedByCurrentUser(path, info)
		},
	})
	dir := newDir(t, dirs, testSpec)
	assert.Equal(t, parentOf(own), filepath.Dir(dir.Path()))
	assert.DirExists(t, theirs, "the sweep touches no directory of another user")
	assert.Contains(t, logs.String(), "skip a base of the agent directories")
	assert.Contains(t, logs.String(), "another user owns the directory")

	only := startDirs(t, Config{
		Specs:       []Spec{testSpec},
		Bases:       []string{foreign},
		ownedByUser: func(string, fs.FileInfo) (bool, error) { return false, nil },
	})
	_, err := only.New(context.Background(), testSpec)
	require.ErrorIs(t, err, errForeignParent, "with no other base, the error states why")
}

func TestNewTakesTheNextBaseWhenTheParentIsNotADirectory(t *testing.T) {
	t.Parallel()
	blocked, open := newBase(t), newBase(t)
	require.NoError(t, os.WriteFile(parentOf(blocked), nil, 0o600))
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{blocked, open}})
	dir := newDir(t, dirs, testSpec)
	assert.Equal(t, parentOf(open), filepath.Dir(dir.Path()))
}

// A base too long for the provider's socket gives way to the next one, and
// nothing is created under it.
func TestNewSkipsABaseTooLongForTheSocket(t *testing.T) {
	t.Parallel()
	spec := Spec{Prefix: "sock", SocketName: "bridge.sock"}
	short := newShortBase(t)
	long := filepath.Join(newBase(t), strings.Repeat("d", MaxSocketPathBytes()))
	require.NoError(t, os.Mkdir(long, 0o700))
	dirs := startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{long, short}})
	dir := newDir(t, dirs, spec)
	assert.Equal(t, parentOf(short), filepath.Dir(dir.Path()))
	assert.LessOrEqual(t, len(filepath.Join(dir.Path(), spec.SocketName)), MaxSocketPathBytes())
	assert.NoDirExists(t, parentOf(long), "nothing is created under a base that does not fit")

	only := startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{long}})
	_, err := only.New(context.Background(), spec)
	require.ErrorContains(t, err, "the limit is "+strconv.Itoa(MaxSocketPathBytes()))
}

// newShortBase creates a base short enough for a socket path on every
// platform.
func newShortBase(t *testing.T) string {
	t.Helper()
	parent := ""
	if runtime.GOOS != "windows" {
		parent = "/tmp"
	}
	dir, err := os.MkdirTemp(parent, "ad")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestCloseRemovesTheDirectoryAndReleasesItsLock(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{base}})
	dir, err := dirs.New(context.Background(), testSpec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir.Path(), "attachments", "1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir.Path(), "attachments", "1", "a.txt"), []byte("x"), 0o600))

	require.NoError(t, dir.Close())
	assert.NoDirExists(t, dir.Path())
	require.NoError(t, dir.Close(), "a second close returns the first result")
	var none *Dir
	require.NoError(t, none.Close())

	entries, err := os.ReadDir(parentOf(base))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// createLock fails when the sweep of another worker took the new directory
// first, so New makes another directory.
func TestCreateLockLosesToASweepThatCameFirst(t *testing.T) {
	t.Parallel()
	created := filepath.Join(t.TempDir(), lockFileName)
	require.NoError(t, os.WriteFile(created, nil, 0o600))
	_, err := createLock(created)
	require.ErrorIs(t, err, errLockTaken, "the sweep created the lock file")

	removed := filepath.Join(t.TempDir(), "gone", lockFileName)
	_, err = createLock(removed)
	require.ErrorIs(t, err, errLockTaken, "the sweep removed the directory")
}

func TestClaimLockReportsAHeldLockAndAMissingDirectory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), lockFileName)
	lock, err := createLock(path)
	require.NoError(t, err)
	_, held, err := claimLock(path)
	require.NoError(t, err)
	assert.False(t, held, "a second open file cannot take the lock, in this process either")
	require.NoError(t, lock.release())

	_, held, err = claimLock(filepath.Join(t.TempDir(), "gone", lockFileName))
	require.NoError(t, err)
	assert.False(t, held, "a directory that went away is nothing to sweep")
}

func TestStillAtComparesTheOpenFileWithThePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	lock, err := createLock(path)
	require.NoError(t, err)
	assert.True(t, stillAt(lock.file, path))

	require.NoError(t, lock.release())
	lock, err = createLock(path + ".2")
	require.NoError(t, err)
	defer func() { _ = lock.release() }()
	require.NoError(t, os.Remove(path+".2"))
	assert.False(t, stillAt(lock.file, path+".2"), "the file lost its name")
	if runtime.GOOS == "windows" {
		// Windows can keep the name of a removed file while a handle holds it
		// open, and then refuses a new file with that name.
		return
	}
	require.NoError(t, os.WriteFile(path+".2", nil, 0o600))
	assert.False(t, stillAt(lock.file, path+".2"), "another file took the name")
}

func TestSpecOfParsesTheNameOfADirectory(t *testing.T) {
	t.Parallel()
	d := &Dirs{specs: map[string]Spec{"cline": {Prefix: "cline"}}}
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"cline-123-456", true},
		{"cline-1-0", true},
		{"cline-123", false},
		{"cline--456", false},
		{"cline-12a-456", false},
		{"cline-123-45x", false},
		{"amp-123-456", false},
		{"cline", false},
		{"clinex-1-2", false},
	} {
		require.NoError(t, os.Mkdir(filepath.Join(root, tc.name), 0o700))
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		var entry fs.DirEntry
		for _, e := range entries {
			if e.Name() == tc.name {
				entry = e
			}
		}
		require.NotNil(t, entry)
		_, ok := d.specOf(entry)
		assert.Equalf(t, tc.want, ok, "%s", tc.name)
	}
}

func TestUsableBasesKeepsTheFirstOfEachDirectoryAndDropsARelativeOne(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	assert.Equal(t, []string{a, b}, usableBases([]string{a, "relative", b, a + string(filepath.Separator), filepath.Join(b, ".")}))
	assert.Empty(t, usableBases(nil))
}

func TestMaxSocketPathBytes(t *testing.T) {
	t.Parallel()
	switch runtime.GOOS {
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "ios":
		assert.Equal(t, 103, MaxSocketPathBytes())
	default:
		assert.Equal(t, 107, MaxSocketPathBytes())
	}
}

// The socket path may take the whole limit and not one byte more. New measures
// the longest name that os.MkdirTemp can give, so every directory under an
// accepted base holds the socket.
func TestNewAcceptsASocketPathThatTakesTheWholeLimit(t *testing.T) {
	t.Parallel()
	spec := Spec{Prefix: "sock", SocketName: "s"}
	short := newShortBase(t)
	// tail is the part of the longest socket path that follows the base: the
	// separator, the parent, the longest directory name and the socket.
	tail := 1 + len(filepath.Join(parentName(), namePattern(spec)+maxRandomSuffix, spec.SocketName))
	fill := MaxSocketPathBytes() - tail - len(short) - 1
	require.Positive(t, fill, "the short base leaves room to fill")
	exact := filepath.Join(short, strings.Repeat("d", fill))
	over := filepath.Join(short, strings.Repeat("e", fill+1))
	require.NoError(t, os.Mkdir(exact, 0o700))
	require.NoError(t, os.Mkdir(over, 0o700))
	require.Equal(t, MaxSocketPathBytes(), len(exact)+tail)

	dirs := startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{exact}})
	dir := newDir(t, dirs, spec)
	assert.Equal(t, parentOf(exact), filepath.Dir(dir.Path()))
	assert.LessOrEqual(t, len(filepath.Join(dir.Path(), spec.SocketName)), MaxSocketPathBytes())

	refused := startDirs(t, Config{Specs: []Spec{spec}, Bases: []string{over}})
	_, err := refused.New(context.Background(), spec)
	require.ErrorContains(t, err, "is "+strconv.Itoa(MaxSocketPathBytes()+1)+" bytes")
	assert.NoDirExists(t, parentOf(over), "nothing is created under a base one byte too long")
}

// The sweep hands its context to each hook. A worker whose context ends while
// it sweeps ends the hooks at once, and their directories stay for the next
// sweep.
func TestAnEndedContextEndsTheStaleHooks(t *testing.T) {
	t.Parallel()
	base := newBase(t)
	kept := staleDir(t, base, testSpec, 1, true)
	hookErr := make(chan error, 1)
	spec := testSpec
	spec.OnStale = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		hookErr <- ctx.Err()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dirs, err := Start(ctx, Config{Specs: []Spec{spec}, Bases: []string{base}, Clock: testutil.NewQuartzMock(t)})
	require.NoError(t, err)
	waitSwept(t, dirs)
	select {
	case err := <-hookErr:
		require.ErrorIs(t, err, context.Canceled, "the hook sees the context of the worker")
	default:
		t.Fatal("the sweep ended before the hook returned")
	}
	assert.DirExists(t, kept, "a hook that the context ended keeps its directory")
	assert.FileExists(t, filepath.Join(kept, "secret.json"))
	claimed, held, err := claimLock(filepath.Join(kept, lockFileName))
	require.NoError(t, err)
	assert.True(t, held, "the sweep released the lock of the directory that it kept")
	require.NoError(t, claimed.release())
}

// An owner that cannot be read is not an owner that the check accepts. New
// takes the next base, and the sweep leaves that parent alone.
func TestNewTakesTheNextBaseWhenTheOwnerCannotBeRead(t *testing.T) {
	t.Parallel()
	unreadable, own := newBase(t), newBase(t)
	theirs := staleDir(t, unreadable, testSpec, 1, true)
	readFails := func(path string, info fs.FileInfo) (bool, error) {
		if path == parentOf(unreadable) {
			return false, errors.New("access denied")
		}
		return ownedByCurrentUser(path, info)
	}
	dirs := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{unreadable, own}, ownedByUser: readFails})
	dir := newDir(t, dirs, testSpec)
	assert.Equal(t, parentOf(own), filepath.Dir(dir.Path()))
	assert.DirExists(t, theirs, "the sweep leaves a parent whose owner it cannot read")

	only := startDirs(t, Config{Specs: []Spec{testSpec}, Bases: []string{unreadable}, ownedByUser: readFails})
	_, err := only.New(context.Background(), testSpec)
	require.ErrorContains(t, err, "read the owner of "+parentOf(unreadable))
	require.ErrorContains(t, err, "access denied")
}

// setTempDir points the system's temporary directory at dir for one test.
func setTempDir(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
		return
	}
	t.Setenv("TMPDIR", dir)
}

// The default bases, the preferred one first: $XDG_RUNTIME_DIR, the "run"
// directory under the data directory, the system's temporary directory, and
// /tmp on Unix. t.Setenv keeps the test serial.
func TestPrepareBasesListsTheDefaultBasesInOrder(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	setTempDir(t, t.TempDir())
	dataDir := t.TempDir()

	bases := prepareBases(dataDir)

	run := filepath.Join(dataDir, runDirName)
	want := []string{xdg, run, os.TempDir()}
	if runtime.GOOS != "windows" {
		want = append(want, "/tmp")
	}
	assert.Equal(t, want, bases)
	info, err := os.Stat(run)
	require.NoError(t, err, "prepareBases creates the base under the data directory")
	assert.True(t, info.IsDir())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
}

// A relative $XDG_RUNTIME_DIR would put the parent under the worker's working
// directory, and a data directory that cannot hold "run" holds no base. Both
// are left out, and the system's temporary directory comes first.
func TestPrepareBasesLeavesOutABaseThatCannotHoldAParent(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join("relative", "runtime"))
	setTempDir(t, t.TempDir())
	blocked := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))

	bases := prepareBases(blocked)
	require.NotEmpty(t, bases)
	assert.Equal(t, os.TempDir(), bases[0])
	for _, base := range bases {
		assert.True(t, filepath.IsAbs(base), base)
		assert.NotContains(t, base, blocked)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	assert.Equal(t, os.TempDir(), prepareBases("")[0], "no data directory states no base under it")
}
