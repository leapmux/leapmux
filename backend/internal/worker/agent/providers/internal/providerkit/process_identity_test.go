package providerkit

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// waitHelperEnv makes this test binary a process that waits until its stdin
// closes and then exits with status 0.
const waitHelperEnv = "LEAPMUX_TEST_PROVIDERKIT_WAIT"

// TestHelperProcessWaitsForStdin is the process that the identity tests start.
// It does nothing in the test process itself.
func TestHelperProcessWaitsForStdin(t *testing.T) {
	if os.Getenv(waitHelperEnv) != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// waitingProcess is a running helper process. A goroutine reaps it the moment
// it exits, so an ended helper no longer holds its pid.
type waitingProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan struct{}
}

func startWaitingProcess(t *testing.T) *waitingProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessWaitsForStdin$")
	cmd.Env = append(os.Environ(), waitHelperEnv+"=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready", strings.TrimSpace(line))
	p := &waitingProcess{cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		<-p.exited
	})
	return p
}

// waitExit waits until the process exited and a goroutine reaped it.
func (p *waitingProcess) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-p.exited:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the helper process did not exit")
	}
}

// endedByItself closes the stdin of a process that still runs and reports
// whether it then ended with status 0. A process that something killed before
// ended with another status.
func (p *waitingProcess) endedByItself(t *testing.T) bool {
	t.Helper()
	_ = p.stdin.Close()
	p.waitExit(t)
	return p.cmd.ProcessState.Success()
}

func TestIdentifyProcessIdentifiesARunningProcess(t *testing.T) {
	t.Parallel()
	self, ok := IdentifyProcess(os.Getpid())
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), self.PID)
	assert.Positive(t, self.StartTime)
	assert.LessOrEqual(t, self.StartTime, time.Now().UnixMilli())
	assert.True(t, self.Runs())
	again, ok := IdentifyProcess(os.Getpid())
	require.True(t, ok)
	assert.Equal(t, self, again, "the start time of one process does not change")
}

func TestIdentifyProcessFindsNoProcess(t *testing.T) {
	t.Parallel()
	ended := startWaitingProcess(t)
	require.True(t, ended.endedByItself(t))
	// A pid past the range of a pid on any platform. The variable keeps the
	// conversion out of constant arithmetic, which a 32-bit int refuses.
	tooLarge := int64(math.MaxInt32) + 1
	for _, pid := range []int{0, -1, ended.cmd.Process.Pid, int(tooLarge)} {
		_, ok := IdentifyProcess(pid)
		assert.Falsef(t, ok, "pid %d", pid)
	}
}

func TestProcessIdentityRunsUntilItsProcessEnds(t *testing.T) {
	t.Parallel()
	child := startWaitingProcess(t)
	identity, ok := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, ok)
	assert.True(t, identity.Runs())
	require.True(t, child.endedByItself(t))
	assert.False(t, identity.Runs())
}

// C-L1: a pid whose process runs, but whose start time is not the one that the
// worker verified, belongs to another process. Kill leaves it alone.
func TestProcessIdentityKillsNoProcessWithAnotherStartTime(t *testing.T) {
	t.Parallel()
	child := startWaitingProcess(t)
	current, ok := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, ok)
	earlier := ProcessIdentity{PID: current.PID, StartTime: current.StartTime - 1}
	assert.False(t, earlier.Runs(), "the pid runs another process")
	killed, err := earlier.Kill()
	require.NoError(t, err)
	assert.False(t, killed)
	assert.True(t, child.endedByItself(t), "nothing killed the process that holds the pid now")
}

func TestProcessIdentityKillsTheProcessThatItIdentifies(t *testing.T) {
	t.Parallel()
	child := startWaitingProcess(t)
	identity, ok := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, ok)
	killed, err := identity.Kill()
	require.NoError(t, err)
	assert.True(t, killed)
	child.waitExit(t)
	assert.False(t, child.cmd.ProcessState.Success(), "the kill ended the process")
	assert.False(t, identity.Runs())
	killed, err = identity.Kill()
	require.NoError(t, err)
	assert.False(t, killed, "a process that ended is not killed again")
}

func TestProcessIdentityNeverKillsTheWorker(t *testing.T) {
	t.Parallel()
	self, ok := IdentifyProcess(os.Getpid())
	require.True(t, ok)
	killed, err := self.Kill()
	require.NoError(t, err)
	assert.False(t, killed)
}

func TestTheZeroProcessIdentityIdentifiesNoProcess(t *testing.T) {
	t.Parallel()
	var none ProcessIdentity
	assert.True(t, none.IsZero())
	assert.False(t, none.Runs())
	killed, err := none.Kill()
	require.NoError(t, err)
	assert.False(t, killed)
}

func TestProcessRuns(t *testing.T) {
	t.Parallel()
	assert.True(t, ProcessRuns(os.Getpid()))
	ended := startWaitingProcess(t)
	require.True(t, ended.endedByItself(t))
	assert.False(t, ProcessRuns(ended.cmd.Process.Pid))
	assert.False(t, ProcessRuns(0), "no process has pid zero")
	assert.False(t, ProcessRuns(-1), "a negative pid selects a group, never one process")
}
