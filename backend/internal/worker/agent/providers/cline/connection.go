package cline

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The private hub daemon.
//
// Each agent runs one Cline hub daemon as a foreground child, and owns its
// whole lifetime. The daemon shares the user's own Cline data directory -- the
// account, the providers and the session history -- and keeps these private:
//
//   - The port, which the worker chooses and states both on the command line
//     and in CLINE_HUB_PORT, so that a `cline` a tool runs finds this daemon
//     and never the user's own hub on the default port.
//   - The discovery record and, with it, the daemon's instance lock. Both
//     derive from CLINE_HUB_DISCOVERY_PATH, which points into the agent's
//     private directory. The lock is what lets several daemons share the data
//     directory, and the record holds the auth token.
//   - The agenda task database (CLINE_TASKS_DB_PATH). Each daemon marks every
//     running agenda run of its database interrupted when it starts, so a
//     shared database would let an agent's start end the user's own runs.
//
// The private values reach the daemon through the shell wrapper's SetEnv,
// after the user's profile runs, so a profile export cannot move them.
//
// The daemon never starts or joins another hub: its entry starts its own
// server and nothing else, and --no-connectors keeps it from supervising the
// user's chat connectors. CLINE_SESSION_BACKEND_MODE=local keeps a `cline`
// that a tool runs on its own local runtime as well. The daemon starts with
// CLINE_RUN_AS_HUB_DAEMON alone and without the `--cline-hub-daemon` flag,
// which it ignores: `cline doctor fix` kills every process whose command line
// holds that flag.
//
// What the daemon shares with the user's own hub, and what that costs, is in
// doc.go.

// The files of the agent's private directory.
const (
	discoveryFileName = "hub.json"
	tasksDBFileName   = "tasks.db"
	attachmentsDir    = "attachments"
)

// hubListen is where the private daemon listens: the host string that the
// worker states on the daemon's command line, and the loopback address that
// the daemon then binds.
//
// Cline opens a connection with no token when the request states a local
// Origin and the daemon's host string is `localhost`, `127.0.0.1`, `::1` or
// `[::1]` (isLocalHubHostName in hub-websocket-server.ts of Cline 3.0.64). Any
// local process can send that Origin, so the worker starts the daemon on a
// loopback host that is not in that list:
//
//   - Linux and Windows route all of 127.0.0.0/8 to loopback, so the daemon
//     binds 127.0.0.2.
//   - macOS has 127.0.0.1 alone on lo0, and a bind to 127.0.0.2 fails (Cline
//     reports it as EADDRINUSE). The host `127.1` is the short form of
//     127.0.0.1: the resolver binds 127.0.0.1, and Cline's URL states
//     `ws://127.0.0.1:<port>/hub`.
//
// The long IPv6 form `0:0:0:0:0:0:0:1` is impossible here: Cline builds its URL
// from the host string, and a URL cannot hold an IPv6 address without brackets,
// so the daemon exits at start. The start also checks the rule directly
// (refuseTokenlessHub), so a Cline release that changes the list cannot open
// the daemon to other processes unseen.
type hubListen struct {
	host    string
	address string
}

// hubListenFor is the listen host and address on the operating system goos.
func hubListenFor(goos string) hubListen {
	if goos == "darwin" {
		return hubListen{host: "127.1", address: "127.0.0.1"}
	}
	return hubListen{host: "127.0.0.2", address: "127.0.0.2"}
}

// daemonListen is where the daemon listens on this operating system.
var daemonListen = hubListenFor(runtime.GOOS)

// The daemon's timing.
const (
	// discoveryPollInterval is how often the start reads the discovery record
	// while it waits for the daemon.
	discoveryPollInterval = 100 * time.Millisecond
	// shutdownRequestTimeout limits the authenticated shutdown request.
	shutdownRequestTimeout = 2 * time.Second
	// statusRequestTimeout limits the authenticated status request that
	// verifies the daemon's process before the shutdown.
	statusRequestTimeout = 2 * time.Second
	// daemonExitWait limits how long the worker waits for a daemon to exit
	// after the shutdown request: at a stop, and in the hook of a stale agent
	// directory. The daemon forces its own exit 2 seconds after a shutdown
	// starts.
	daemonExitWait = 10 * time.Second
	// daemonExitPoll is how often that wait checks whether the daemon runs.
	daemonExitPoll = 50 * time.Millisecond
)

// discoveryRecord is the daemon's discovery record: where it listens, the
// token it takes, and the protocol versions it speaks.
type discoveryRecord struct {
	HubID                    string `json:"hubId"`
	ProtocolVersion          string `json:"protocolVersion"`
	MinClientProtocolVersion string `json:"minClientProtocolVersion"`
	MaxClientProtocolVersion string `json:"maxClientProtocolVersion"`
	CoreVersion              string `json:"coreVersion"`
	AuthToken                string `json:"authToken"`
	Host                     string `json:"host"`
	Port                     int    `json:"port"`
	URL                      string `json:"url"`
	PID                      int    `json:"pid"`
}

// ready reports whether the record states everything a client needs.
func (r discoveryRecord) ready() bool {
	return r.AuthToken != "" && r.URL != "" && r.PID > 0
}

// minimumVersionHint states the Cline release that this provider drives, for
// every error that a daemon it cannot drive produces.
const minimumVersionHint = "LeapMux drives Cline through its hub protocol v1, which Cline 3.0.64 speaks; install Cline 3.0.64 or later, and make sure the `cline` on PATH is that version"

// errProtocolMismatch reports a daemon that speaks no hub protocol this
// provider can drive.
var errProtocolMismatch = errors.New("the Cline hub speaks a protocol that LeapMux cannot drive")

// hubProtocolNumber reads `vN`.
var hubProtocolNumber = regexp.MustCompile(`^v(\d+)$`)

// minimumCoreVersion is the version of Cline's core in Cline 3.0.64
// (sdk/packages/core/package.json). An older core can speak hub protocol v1
// and still ignore what keeps the daemon private: CLINE_TASKS_DB_PATH and
// --no-connectors. Its start would then mark the user's own agenda runs
// interrupted.
var minimumCoreVersion = []int{0, 0, 85}

// releaseComponents reads the release numbers of a version as Cline's own
// parseReleaseComponents does: the numbers before any prerelease or build
// suffix. It reports false for a version with no such numbers.
func releaseComponents(version string) ([]int, bool) {
	release, _, _ := strings.Cut(strings.TrimSpace(version), "+")
	release, _, _ = strings.Cut(release, "-")
	if release == "" {
		return nil, false
	}
	parts := strings.Split(release, ".")
	components := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}
		components = append(components, n)
	}
	return components, true
}

// compareReleases orders two releases as Cline's compareReleaseComponents
// does: a component that one release does not state counts as 0.
func compareReleases(a, b []int) int {
	for i := range max(len(a), len(b)) {
		var left, right int
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			return cmp.Compare(left, right)
		}
	}
	return 0
}

// checkProtocol refuses a daemon that LeapMux cannot drive:
//   - A daemon whose range of client protocol versions leaves v1 out. The
//     check follows Cline's own isHubProtocolCompatible: a limit that the
//     record does not state takes the daemon's own version.
//   - A daemon whose core is older than minimumCoreVersion, or that states no
//     core version.
func checkProtocol(record discoveryRecord) error {
	parse := func(version string) (int, bool) {
		match := hubProtocolNumber.FindStringSubmatch(strings.TrimSpace(version))
		if match == nil {
			return 0, false
		}
		n, err := strconv.Atoi(match[1])
		return n, err == nil
	}
	own, ok := parse(record.ProtocolVersion)
	if !ok {
		return fmt.Errorf("%w: its discovery record states no protocol version (%q); %s", errProtocolMismatch, record.ProtocolVersion, minimumVersionHint)
	}
	low, high := own, own
	if n, ok := parse(record.MinClientProtocolVersion); ok {
		low = n
	}
	if n, ok := parse(record.MaxClientProtocolVersion); ok {
		high = n
	}
	want, _ := parse(hubProtocolVersion)
	if want < low || want > high {
		return fmt.Errorf("%w: it accepts clients of protocol v%d to v%d, and LeapMux speaks %s; %s",
			errProtocolMismatch, low, high, hubProtocolVersion, minimumVersionHint)
	}
	core, ok := releaseComponents(record.CoreVersion)
	if !ok || compareReleases(core, minimumCoreVersion) < 0 {
		return fmt.Errorf("%w: its core version is %q, older than the core of Cline 3.0.64; %s",
			errProtocolMismatch, record.CoreVersion, minimumVersionHint)
	}
	return nil
}

// daemonArgs are the daemon's arguments. The daemon listens on a loopback
// address alone (daemonListen), at the port the worker chose.
func daemonArgs(workingDir string, port int) []string {
	return []string{
		flagCwd, workingDir,
		flagHost, daemonListen.host,
		flagPort, strconv.Itoa(port),
		flagPathname, hubPathname,
		flagNoConnectors,
	}
}

// daemonSetEnv are the variables that the shell wrapper sets after the user's
// profile runs.
func daemonSetEnv(dir string, port int) []string {
	return append([]string{
		envRunAsHubDaemon + "=1",
		envHubDiscoveryPath + "=" + filepath.Join(dir, discoveryFileName),
		envHubPort + "=" + strconv.Itoa(port),
		envTasksDBPath + "=" + filepath.Join(dir, tasksDBFileName),
	}, localRuntimeEnv...)
}

// freePort returns a loopback port that nothing listens on at the moment. The
// daemon binds it a moment later; the one start that loses the port to another
// process in between retries once with a new one (see startDaemonProcess).
func freePort() (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(daemonListen.address, "0"))
	if err != nil {
		return 0, fmt.Errorf("find a free port for the Cline hub: %w", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// daemonStart is one started daemon process and the record it published.
type daemonStart struct {
	process *providerkit.Process
	record  discoveryRecord
}

// errAddressInUse marks a daemon that exited because another process took its
// port first.
var errAddressInUse = errors.New("the port of the Cline hub is in use")

// startDaemonProcess starts the daemon and waits for its discovery record. It
// tries a second port once when another process took the first one in the
// moment between the choice and the bind.
func startDaemonProcess(ctx context.Context, opts agent.Options, spec launch.Spec, dir string, clock quartz.Clock) (*daemonStart, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		port, err := freePort()
		if err != nil {
			return nil, err
		}
		started, err := startDaemonOnPort(ctx, opts, spec, dir, port, clock)
		if err == nil {
			return started, nil
		}
		lastErr = err
		if !errors.Is(err, errAddressInUse) {
			return nil, err
		}
		slog.Info("cline hub port was taken; retrying with another", "agent_id", opts.AgentID, "port", port)
	}
	return nil, lastErr
}

// startDaemonOnPort starts one daemon on port and waits for its discovery
// record.
func startDaemonOnPort(ctx context.Context, opts agent.Options, spec launch.Spec, dir string, port int, clock quartz.Clock) (*daemonStart, error) {
	procCtx, cancel := context.WithCancel(context.Background())
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(procCtx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		SetEnv:     daemonSetEnv(dir, port),
		BaseArgs:   daemonArgs(opts.WorkingDir, port),
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderr, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	process := providerkit.NewProcess(opts, "cline", cmd, stdin, procCtx, cancel, preambleDelimiter, metaPrefix)
	if err := process.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	process.DrainStderr(stderr)
	// The daemon prints nothing that the worker reads on stdout, but the pipe
	// must drain, or a daemon that logs there would block.
	go process.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
		slog.Debug("cline hub output", "agent_id", opts.AgentID, "line", string(line))
	})

	record, err := waitForDiscovery(ctx, filepath.Join(dir, discoveryFileName), process.ProcessDone(), opts.EffectiveStartupTimeout(), clock)
	if err != nil {
		process.Stop()
		_ = process.Wait()
		stderrText := strings.TrimSpace(process.Stderr())
		if strings.Contains(stderrText, "EADDRINUSE") {
			return nil, fmt.Errorf("%w: %s", errAddressInUse, stderrText)
		}
		return nil, process.FormatStartupError("hub start", fmt.Errorf("%w; %s", err, minimumVersionHint))
	}
	if err := checkProtocol(record); err != nil {
		stopDaemonAt(record)
		process.Stop()
		_ = process.Wait()
		return nil, err
	}
	return &daemonStart{process: &process, record: record}, nil
}

// errDaemonExited reports a daemon that exited before it published its record.
var errDaemonExited = errors.New("the Cline hub exited before it was ready")

// waitForDiscovery reads the discovery record until it states an address and a
// token, the process exits, or the timeout passes. The daemon writes the record
// by renaming a complete file into place, so a read never sees half of one. The
// next poll reads a record again that does not parse yet.
func waitForDiscovery(ctx context.Context, path string, processDone <-chan struct{}, timeout time.Duration, clock quartz.Clock) (discoveryRecord, error) {
	deadline := clock.NewTimer(timeout, "cline", "discovery-deadline")
	defer deadline.Stop()
	ticker := clock.NewTicker(discoveryPollInterval, "cline", "discovery-poll")
	defer ticker.Stop()
	for {
		if record, ok := readDiscovery(path); ok {
			return record, nil
		}
		select {
		case <-ticker.C:
		case <-processDone:
			// The record can land in the same moment as the exit of a daemon that
			// failed a later step; the exit decides.
			return discoveryRecord{}, errDaemonExited
		case <-deadline.C:
			return discoveryRecord{}, fmt.Errorf("the Cline hub did not publish its address within %s", timeout)
		case <-ctx.Done():
			return discoveryRecord{}, ctx.Err()
		}
	}
}

// readDiscovery reads the record at path, and reports whether it states
// everything a client needs.
func readDiscovery(path string) (discoveryRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return discoveryRecord{}, false
	}
	var record discoveryRecord
	if err := json.Unmarshal(data, &record); err != nil || !record.ready() {
		return discoveryRecord{}, false
	}
	return record, true
}

// hubEndpoint returns the loopback HTTP endpoint of the daemon and the path of
// its WebSocket.
func hubEndpoint(record discoveryRecord) (*providerkit.HTTPEndpoint, string, error) {
	parsed, err := providerkit.ParseLoopbackHTTPURL(strings.Replace(record.URL, "ws://", "http://", 1))
	if err != nil {
		return nil, "", fmt.Errorf("the Cline hub states an address that is not a loopback address: %w", err)
	}
	path := parsed.Path
	if path == "" {
		path = hubPathname
	}
	parsed.Path = ""
	endpoint, err := providerkit.NewHTTPEndpoint(parsed.String(), providerkit.BearerAuth(record.AuthToken))
	if err != nil {
		return nil, "", err
	}
	return endpoint, path, nil
}

// errHubTrustsLocalOrigin refuses a daemon that opens a connection with no
// token for a request that states a local Origin.
var errHubTrustsLocalOrigin = errors.New("the Cline hub accepts a connection without its token from any local process that states a localhost origin, so another user or a web page on this machine could drive the agent and answer its approvals; LeapMux does not run such a hub")

// localOrigin is the Origin that Cline counts as local.
const localOrigin = "http://localhost"

// refuseTokenlessHub opens the daemon's WebSocket with no token and a local
// Origin, which any local process can send, and fails when the daemon accepts
// it. The daemon must be ready: the start calls it after its own connection
// opened.
//
// Only a refusal that the daemon states passes: an HTTP error, or a connection
// that the daemon ends. A check that ends with no answer fails the start,
// because it shows nothing.
func refuseTokenlessHub(ctx context.Context, record discoveryRecord) error {
	parsed, err := providerkit.ParseLoopbackHTTPURL(strings.Replace(record.URL, "ws://", "http://", 1))
	if err != nil {
		return fmt.Errorf("the Cline hub states an address that is not a loopback address: %w", err)
	}
	path := parsed.Path
	if path == "" {
		path = hubPathname
	}
	parsed.Path = ""
	// No credential: the check is what a process without the token can do.
	endpoint, err := providerkit.NewHTTPEndpoint(parsed.String(), nil)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	conn, err := endpoint.OpenWebSocket(ctx, path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{localOrigin}},
	})
	if err == nil {
		_ = conn.CloseNow()
		return errHubTrustsLocalOrigin
	}
	if ctx.Err() != nil {
		return fmt.Errorf("check that the Cline hub requires its token: %w", ctx.Err())
	}
	return nil
}

// stopDaemonAt asks the daemon of record to shut down, with its own token, and
// returns the daemon's process as far as the worker verified it before the
// request (see verifyDaemon). The daemon then runs its graceful shutdown, which
// it limits by itself. It returns once the daemon accepted the request or the
// request failed.
func stopDaemonAt(record discoveryRecord) providerkit.ProcessIdentity {
	return shutdownDaemon(context.Background(), record)
}

// shutdownDaemon is stopDaemonAt for a caller whose work ends with ctx.
func shutdownDaemon(ctx context.Context, record discoveryRecord) providerkit.ProcessIdentity {
	endpoint, _, err := hubEndpoint(record)
	if err != nil {
		slog.Debug("cline hub shutdown skipped", "error", err)
		return providerkit.ProcessIdentity{}
	}
	defer endpoint.Close()
	daemon := verifyDaemon(ctx, endpoint, record)
	requestCtx, cancel := context.WithTimeout(ctx, shutdownRequestTimeout)
	defer cancel()
	if err := endpoint.Do(requestCtx, http.MethodPost, "/shutdown", nil, nil); err != nil {
		slog.Debug("cline hub shutdown request", "hub_id", record.HubID, "error", err)
	}
	return daemon
}

// hubStatus is the part of the answer of the daemon's `/status` that
// verifyDaemon reads. Cline 3.0.64 answers with its whole discovery record.
type hubStatus struct {
	HubID string `json:"hubId"`
	PID   int    `json:"pid"`
}

// verifyDaemon returns the identity of the process of the daemon that record
// states, or the zero identity when it cannot verify it.
//
// It reads the start time of the record's pid first, and then asks the
// daemon's `/status`, which takes the record's token. An answer that states the
// record's pid and hub id comes from the daemon, because the token reaches no
// other process. The daemon held that pid from its start until that answer,
// so the start time read before it is the daemon's own. A pid alone could
// belong to any process by now.
//
// A daemon that no longer answers cannot be verified. One that began its
// shutdown closed its listener, and Cline 3.0.64 ends that process by itself
// within 2 seconds (HUB_DAEMON_SHUTDOWN_DEADLINE_MS). The discovery record
// states no start time of the process, so nothing else can verify it.
func verifyDaemon(ctx context.Context, endpoint *providerkit.HTTPEndpoint, record discoveryRecord) providerkit.ProcessIdentity {
	if record.HubID == "" {
		return providerkit.ProcessIdentity{}
	}
	identity, ok := providerkit.IdentifyProcess(record.PID)
	if !ok {
		return providerkit.ProcessIdentity{}
	}
	requestCtx, cancel := context.WithTimeout(ctx, statusRequestTimeout)
	defer cancel()
	var status hubStatus
	if err := endpoint.Do(requestCtx, http.MethodGet, "/status", nil, &status); err != nil {
		slog.Debug("cline hub status request", "hub_id", record.HubID, "error", err)
		return providerkit.ProcessIdentity{}
	}
	if status.PID != record.PID || status.HubID != record.HubID {
		slog.Warn("the cline hub states another process than its discovery record",
			"record_pid", record.PID, "record_hub_id", record.HubID, "status_pid", status.PID, "status_hub_id", status.HubID)
		return providerkit.ProcessIdentity{}
	}
	return identity
}

// awaitDaemonExit waits until the daemon's process no longer runs, and kills
// it when the wait passes or ctx ends first. It returns an error when the kill
// fails. It waits for, and kills, only a process that verifyDaemon verified:
// the zero identity returns at once, and the identity of a process that
// ended no longer matches a process that took its pid.
//
// The npm wrapper of `cline` runs the daemon as a child. The wrapper hands the
// child its own stdout, and the worker's reader ends only when every writer of
// that pipe closed it, so the exit of the process the worker started normally
// implies the daemon's. This check covers a wrapper that stops sharing the
// pipe, and the daemon that an ended worker left: a new daemon that resumed
// the session while the old one still ran would give two processes the
// session's files, and each rewrites them whole.
func awaitDaemonExit(ctx context.Context, daemon providerkit.ProcessIdentity, clock quartz.Clock) error {
	if !daemon.Runs() {
		return nil
	}
	deadline := clock.NewTimer(daemonExitWait, "cline", "daemon-exit")
	defer deadline.Stop()
	ticker := clock.NewTicker(daemonExitPoll, "cline", "daemon-exit-poll")
	defer ticker.Stop()
	for daemon.Runs() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			return killDaemon(daemon)
		case <-ctx.Done():
			return killDaemon(daemon)
		}
	}
	return nil
}

// killDaemon kills the verified process of a daemon that did not exit after
// its shutdown.
func killDaemon(daemon providerkit.ProcessIdentity) error {
	slog.Warn("cline hub did not exit after the shutdown; killing it", "pid", daemon.PID)
	if _, err := daemon.Kill(); err != nil {
		return fmt.Errorf("kill the cline hub %d: %w", daemon.PID, err)
	}
	return nil
}

// agentDirSpec states the private directory of each Cline agent: the
// discovery record with the daemon's token, the private task database and the
// attached files. The daemon outlives a worker that ends without stopping it,
// so the hook of a stale directory ends that daemon first.
func agentDirSpec() agentdir.Spec {
	return agentdir.Spec{Prefix: "cline", OnStale: staleDaemonStopper{clock: quartz.NewReal()}.stop}
}

// staleDaemonStopper ends the daemon that the directory of an ended worker
// records.
type staleDaemonStopper struct {
	clock quartz.Clock
}

// stop is the hook of a stale agent directory. It asks the daemon that the
// directory records to shut down, and waits until that daemon ended. It kills
// the daemon when it does not end before the wait passes or ctx ends, and only
// when verifyDaemon verified it. The sweep removes the directory after stop
// returns, so a new daemon never resumes a session that the old one still
// writes. An error keeps the directory for the next sweep.
func (s staleDaemonStopper) stop(ctx context.Context, dir string) error {
	record, ok := readDiscovery(filepath.Join(dir, discoveryFileName))
	if !ok {
		return nil
	}
	daemon := shutdownDaemon(ctx, record)
	if daemon.IsZero() {
		return nil
	}
	slog.Info("stopping a cline hub that an ended worker left", "dir", dir, "pid", daemon.PID)
	return awaitDaemonExit(ctx, daemon, s.clock)
}

// newClineAgentDir creates the private directory of one agent. It waits for
// the sweep of its parent, which ended each daemon that a stale directory
// there recorded.
func newClineAgentDir(ctx context.Context, dirs *agentdir.Dirs) (*agentdir.Dir, error) {
	return dirs.New(ctx, agentDirSpec())
}
