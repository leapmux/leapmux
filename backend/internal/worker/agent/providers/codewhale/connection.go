package codewhale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/util/procutil"
)

// The runtime process: its launch, its readiness, and its ownership record.
//
// The runtime refuses port 0, so the worker reserves a loopback port and states
// it. Another process can take that port before the runtime binds it; the
// runtime then exits with "Failed to bind", and the launch retries on a fresh
// port.
//
// The runtime does not read its stdin, so a worker that dies does not end it:
// the orphan keeps its port and, worse, the lock on its store, and every later
// resume of that thread fails on the lock. So the worker records who runs each
// store, and a start that finds the store held by a runtime whose worker is gone
// ends that runtime first. The record states the runtime's process id AND its
// start time, because a process id alone can name an unrelated process after a
// reboot.

// Launch limits.
const (
	// launchAttempts is how many ports one start tries.
	launchAttempts = 3
	// healthPollFirst and healthPollMax pace the readiness poll. The runtime
	// answers /health within a few hundred milliseconds of its listening line.
	healthPollFirst = 20 * time.Millisecond
	healthPollMax   = 500 * time.Millisecond
	// orphanExitWait is how long a start waits for an orphaned runtime to release
	// its store after the worker ended it, and orphanExitPoll paces that wait.
	orphanExitWait = 5 * time.Second
	orphanExitPoll = 20 * time.Millisecond
)

// healthTimerTag labels the readiness poll's timer for a test's clock trap.
const healthTimerTag = "codewhale-health"

// orphanTimerTag labels the orphan wait's timer for a test's clock trap.
const orphanTimerTag = "codewhale-orphan-exit"

// ownerFileName is the ownership record inside a store directory, beside the
// runtime's own `runtime/` directory.
const ownerFileName = "leapmux-owner.json"

// storeOwner records which process runs a store, and which worker started it.
type storeOwner struct {
	RuntimePID        int32 `json:"runtime_pid"`
	RuntimeCreateTime int64 `json:"runtime_create_time_ms"`
	Port              int   `json:"port"`
	WorkerPID         int32 `json:"worker_pid"`
	WorkerCreateTime  int64 `json:"worker_create_time_ms"`
}

// errStoreHeldByLiveWorker reports a store that another running worker owns.
var errStoreHeldByLiveWorker = errors.New("this Codewhale session is open in another LeapMux worker on this machine; close it there first")

// runtimeLaunch is what launchRuntime needs to start one process.
type runtimeLaunch struct {
	opts  agent.Options
	spec  launch.Spec
	store codewhaleStore
	token string
	clock quartz.Clock
}

// launchedRuntime is one runtime that answers its health route.
type launchedRuntime struct {
	agent    *Agent
	endpoint *providerkit.HTTPEndpoint
}

// launchRuntime starts the runtime on a reserved port, and retries on a fresh
// port when another process took the first one.
func launchRuntime(ctx context.Context, sink agent.ProviderServices, l runtimeLaunch) (*launchedRuntime, error) {
	var lastErr error
	for attempt := 0; attempt < launchAttempts; attempt++ {
		launched, err := launchRuntimeOnce(ctx, sink, l)
		if err == nil {
			return launched, nil
		}
		lastErr = err
		if !errors.Is(err, errPortTaken) {
			return nil, err
		}
		slog.Warn("codewhale runtime lost its reserved port; retrying on another", "agent_id", l.opts.AgentID, "attempt", attempt+1)
	}
	return nil, lastErr
}

// errPortTaken reports a runtime that could not bind its reserved port.
var errPortTaken = errors.New("another process took the reserved port")

// launchRuntimeOnce starts one runtime process and waits for it to answer.
func launchRuntimeOnce(ctx context.Context, sink agent.ProviderServices, l runtimeLaunch) (*launchedRuntime, error) {
	port, err := providerkit.ReserveLoopbackPort()
	if err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(processCtx, launch.WrapSpec{
		Shell:      l.opts.Shell,
		LoginShell: l.opts.LoginShell,
		Launch:     l.spec,
		BaseArgs:   []string{"app-server", "--http", "--host", "127.0.0.1", "--port", strconv.Itoa(port)},
		WorkingDir: l.opts.WorkingDir,
	})
	cmd.Env = runtimeEnv(cmd.Environ(), l.opts, l.store, l.token)

	stdin, stdout, stderr, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	a := newAgent(l.opts, sink, l.clock)
	a.Process = providerkit.NewProcess(l.opts, codewhaleProviderName, cmd, stdin, processCtx, cancel, preambleDelimiter, metaPrefix)
	a.stopProcess = cancel
	a.store = l.store
	a.children = newCodewhaleChildren(processCtx)
	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderr)

	waiter := providerkit.NewListenWaiter(listeningLinePattern)
	go a.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
		if !waiter.Observe(line) {
			slog.Debug("codewhale runtime stdout", "agent_id", a.AgentID(), "line", shortText(string(line), 200))
		}
	})

	fail := func(phase string, err error) (*launchedRuntime, error) {
		a.Stop()
		_ = a.Wait()
		if strings.Contains(a.Stderr(), bindFailureText) {
			return nil, errPortTaken
		}
		return nil, a.FormatStartupError(phase, err)
	}

	timeout := l.opts.EffectiveStartupTimeout()
	address, err := waiter.Wait(ctx, a.ProcessDone(), timeout)
	if err != nil {
		return fail("runtime start", err)
	}
	endpoint, err := providerkit.NewHTTPEndpoint(address, providerkit.BearerAuth(l.token))
	if err != nil {
		return fail("runtime address", err)
	}
	a.endpoint = endpoint
	if err := a.waitHealthy(ctx, timeout); err != nil {
		return fail("runtime health", err)
	}
	recordStoreOwner(l.store, cmd.Process.Pid, port)
	return &launchedRuntime{agent: a, endpoint: endpoint}, nil
}

// runtimeEnv builds the runtime's environment.
//
// The token and both store variables are PINNED rather than appended: a worker
// that runs inside a Codewhale session inherits that session's values, and an
// inherited CODEWHALE_RUNTIME_DIR would move the store away from the directory
// LeapMux reads, because it outranks CODEWHALE_TASKS_DIR.
func runtimeEnv(env []string, opts agent.Options, store codewhaleStore, token string) []string {
	env = envutil.PinEnv(env,
		envRuntimeToken+"="+token,
		envTasksDir+"="+store.tasksDir(),
		envRuntimeDir+"="+store.runtimeDir(),
	)
	return providerkit.FinalizeAgentEnv(env, opts)
}

// waitHealthy polls /health until the runtime answers, the process exits, or
// the timeout passes. The listening line comes before the server serves, so a
// request can arrive first.
func (a *Agent) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := a.clock.Now().Add(timeout)
	delay := healthPollFirst
	for {
		requestCtx, cancel := context.WithTimeout(ctx, a.APITimeout())
		err := a.endpoint.Do(requestCtx, http.MethodGet, routeHealth, nil, nil)
		cancel()
		if err == nil {
			return nil
		}
		if a.processExited() {
			return providerkit.ErrServerExited
		}
		if !a.clock.Now().Before(deadline) {
			return fmt.Errorf("the runtime did not answer %s within %s: %w", routeHealth, timeout, err)
		}
		timer := a.clock.NewTimer(delay, healthTimerTag)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-a.ProcessDone():
			timer.Stop()
			return providerkit.ErrServerExited
		}
		delay = min(delay*2, healthPollMax)
	}
}

// readRuntimeInfo reads the runtime's version.
func (a *Agent) readRuntimeInfo() (codewhaleRuntimeInfo, error) {
	var info codewhaleRuntimeInfo
	err := a.call(http.MethodGet, routeRuntimeInfo, nil, nil, &info)
	return info, err
}

// --- ownership ---

// processIdentity reads a process's start time, which pairs with its id to
// identify it across a reboot.
func processIdentity(ctx context.Context, pid int32) (createTime int64, ok bool) {
	proc, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return 0, false
	}
	created, err := proc.CreateTimeWithContext(ctx)
	if err != nil {
		return 0, false
	}
	return created, true
}

// recordStoreOwner writes the store's ownership record. A failure is logged and
// otherwise ignored: the record only helps a later start reclaim an orphan.
func recordStoreOwner(store codewhaleStore, runtimePID, port int) {
	ctx := context.Background()
	owner := storeOwner{RuntimePID: int32(runtimePID), Port: port, WorkerPID: int32(os.Getpid())}
	owner.RuntimeCreateTime, _ = processIdentity(ctx, owner.RuntimePID)
	owner.WorkerCreateTime, _ = processIdentity(ctx, owner.WorkerPID)
	encoded, err := json.Marshal(owner)
	if err != nil {
		return
	}
	path := filepath.Join(store.dir, ownerFileName)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		slog.Debug("codewhale write the store owner", "store", store.dir, "error", err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		slog.Debug("codewhale write the store owner", "store", store.dir, "error", err)
	}
}

// processMatches reports whether pid still runs the process that the record
// states: the same start time, and for a runtime the same command line.
func processMatches(ctx context.Context, pid int32, createTime int64, port int) bool {
	if pid <= 0 || createTime == 0 {
		return false
	}
	proc, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return false
	}
	created, err := proc.CreateTimeWithContext(ctx)
	if err != nil || created != createTime {
		return false
	}
	if port == 0 {
		return true
	}
	args, err := proc.CmdlineSliceWithContext(ctx)
	if err != nil {
		return false
	}
	return slices.Contains(args, "app-server") && slices.Contains(args, strconv.Itoa(port))
}

// reclaimStore ends a runtime that an earlier worker left running on this
// store. It refuses when the worker that started that runtime still runs.
func reclaimStore(ctx context.Context, store codewhaleStore, clock quartz.Clock) error {
	path := filepath.Join(store.dir, ownerFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var owner storeOwner
	if json.Unmarshal(data, &owner) != nil {
		return nil
	}
	if !processMatches(ctx, owner.RuntimePID, owner.RuntimeCreateTime, owner.Port) {
		return nil
	}
	if owner.WorkerPID != int32(os.Getpid()) && processMatches(ctx, owner.WorkerPID, owner.WorkerCreateTime, 0) {
		return errStoreHeldByLiveWorker
	}
	if owner.WorkerPID == int32(os.Getpid()) {
		// This worker runs it: another agent of this worker has the session open.
		// The runtime's own lock error states that when the start runs.
		return nil
	}
	slog.Warn("codewhale ending a runtime that an earlier worker left on its store", "store", store.dir, "pid", owner.RuntimePID)
	job, err := procutil.AssignPID(int(owner.RuntimePID))
	if err != nil {
		return fmt.Errorf("end the orphaned Codewhale runtime %d: %w", owner.RuntimePID, err)
	}
	if err := job.Terminate(); err != nil {
		return fmt.Errorf("end the orphaned Codewhale runtime %d: %w", owner.RuntimePID, err)
	}
	deadline := clock.Now().Add(orphanExitWait)
	for {
		if !processMatches(ctx, owner.RuntimePID, owner.RuntimeCreateTime, owner.Port) {
			return nil
		}
		if !clock.Now().Before(deadline) {
			return fmt.Errorf("the orphaned Codewhale runtime %d did not exit", owner.RuntimePID)
		}
		timer := clock.NewTimer(orphanExitPoll, orphanTimerTag)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}
