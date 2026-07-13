// Package solo provides an in-process launcher for the LeapMux Hub and Worker,
// suitable for solo/desktop mode where both run inside the same binary.
package solo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/worker"
)

// workerSetupPollInterval is how often dev mode re-checks whether the first
// admin user has completed the /setup flow so the auto-registered local
// worker can come online.
const workerSetupPollInterval = 2 * time.Second

// nonLoopbackListenWarnMsg is the security warning emitted when solo mode
// binds to a non-loopback address. Solo mode injects a soloUser into the
// auth interceptor (see hub.NewServer and auth.NewInterceptor), so every
// request is auto-authenticated as the admin without credentials. Loopback
// keeps that contained to the host; anything else hands the admin role to
// anyone who can reach the port.
const nonLoopbackListenWarnMsg = "solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, " +
	"so anyone who can reach this port has full admin access without credentials. " +
	"Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication."

// Config configures the solo launcher.
type Config struct {
	// Listen is the TCP listen address (default: "127.0.0.1:4327").
	Listen string
	// ConfigDir overrides the default config directory.
	ConfigDir string
	// ConfigFile overrides the default config file path.
	ConfigFile string
	// Args are additional CLI flag arguments (passed to hub config loader).
	Args []string
	// CLIFlags restricts which flags are registered (nil = all hub flags).
	CLIFlags []string
	// ExtraFlags registers additional koanf-backed flags; nil uses the
	// desktop-oriented default (encryption-mode, use-login-shell).
	ExtraFlags []hubconfig.ExtraFlagDef
	// DevMode runs in dev mode (binds to all interfaces, logs "dev" banner).
	DevMode bool
	// SkipBanner suppresses the ASCII art banner and access URL.
	SkipBanner bool
	// NoTCP disables the TCP listener. When true, the Hub only listens on
	// the Unix domain socket. This is used by the desktop app to avoid
	// opening a TCP port.
	NoTCP bool
}

// Instance represents a running solo Hub+Worker pair.
type Instance struct {
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	listenURL string
	hubErr    error         // set before hubDone is closed
	hubDone   chan struct{} // closed when the Hub goroutine exits
}

// LocalListenURL returns the URL at which the Hub is accepting local IPC
// connections (unix:<path> on Unix, npipe:<name> on Windows). Callers that
// need to dial the Hub from within the same process tree (e.g. the desktop
// proxy) should use this rather than reconstructing a path.
func (i *Instance) LocalListenURL() string {
	return i.listenURL
}

// Wait blocks until the Hub exits (either via Stop or because it failed
// on its own) and returns its terminal error. Returns nil on clean
// shutdown or http.ErrServerClosed. Safe to call multiple times.
func (i *Instance) Wait() error {
	<-i.hubDone
	return i.hubErr
}

// Stop gracefully shuts down the Hub and Worker and returns the Hub's terminal
// error, including failures from shutdown cleanup such as runtime lease release.
func (i *Instance) Stop() error {
	i.cancel()
	i.wg.Wait()
	return i.Wait()
}

// Start launches a Hub and Worker in-process. It returns an Instance that
// can stop the services and report their terminal cleanup error.
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	logging.Setup()

	modeName := "solo"
	description := "Run Hub + Worker locally for single-user use."
	if cfg.DevMode {
		modeName = "dev"
		description = "Run Hub + Worker together for development."
	}
	flagSetName := "leapmux " + modeName

	defaultListen := cfg.Listen
	if defaultListen == "" {
		defaultListen = "127.0.0.1:4327"
		if cfg.DevMode {
			defaultListen = ":4327"
		}
	}

	configDir := cfg.ConfigDir
	if configDir == "" {
		configDir = "~/.config/leapmux/" + modeName
	}
	configFile := cfg.ConfigFile
	if configFile == "" {
		configFile = configDir + "/" + modeName + ".yaml"
	}

	cliFlags := cfg.CLIFlags
	if cliFlags == nil {
		cliFlags = []string{"listen", "data-dir", "dev-frontend", "storage-sqlite-max-conns", "storage-sqlite-cache-size", "storage-sqlite-mmap-size", "api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds", "log-level", "use-login-shell"}
		if cfg.DevMode {
			cliFlags = append(cliFlags, "public-url")
		}
	}

	extraFlags := cfg.ExtraFlags
	if extraFlags == nil {
		extraFlags = defaultExtraFlags()
	}

	hubCfg, _, err := hubconfig.LoadWithOptions(cfg.Args, hubconfig.LoadOptions{
		DefaultListen:     defaultListen,
		DefaultConfigDir:  configDir,
		DefaultConfigFile: configFile,
		FlagSetName:       flagSetName,
		Description:       description,
		CLIFlags:          cliFlags,
		ExtraFlags:        extraFlags,
		SoloMode:          !cfg.DevMode,
	})
	if err != nil {
		return nil, fmt.Errorf("load hub config: %w", err)
	}
	hubCfg.DevMode = cfg.DevMode

	if shouldWarnNonLoopback(cfg, hubCfg.Listen) {
		slog.Warn(nonLoopbackListenWarnMsg, "listen", hubCfg.Listen)
	}

	level, err := logging.ParseLevel(hubCfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	if cfg.NoTCP {
		hubCfg.Listen = ""
	}

	if !cfg.SkipBanner {
		logging.PrintBanner(modeName)
		logging.PrintBannerURL(hubCfg.PublicURL, hubCfg.Listen)
	}

	// Split data dir into hub and worker subdirectories.
	dataDir := hubCfg.DataDir
	hubCfg.DataDir = filepath.Join(dataDir, "hub")
	workerDataDir := filepath.Join(dataDir, "worker")

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	server, err := hub.NewServer(hubCfg)
	if err != nil {
		return nil, fmt.Errorf("create hub server: %w", err)
	}

	soloCtx, cancel := context.WithCancel(ctx)

	listenURL, err := hubCfg.LocalListenURL()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("resolve local-listen URL: %w", err)
	}
	inst := &Instance{
		cancel:    cancel,
		listenURL: listenURL,
		hubDone:   make(chan struct{}),
	}

	// Start Hub. hubErr/hubDone publish the terminal error to Wait callers.
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		inst.hubErr = server.Serve(soloCtx)
		close(inst.hubDone)
	}()

	// Wait for Hub's local listener (unix socket or named pipe). Race
	// against inst.hubDone so that if Serve returns before the listener
	// is ready, we surface the underlying Serve error (e.g. "bind:
	// invalid argument") instead of the generic "not ready" timeout.
	readyCh := make(chan error, 1)
	go func() { readyCh <- locallisten.WaitReady(soloCtx, listenURL) }()
	select {
	case err := <-readyCh:
		if err != nil {
			cancel()
			inst.wg.Wait()
			if inst.hubErr != nil && !errors.Is(inst.hubErr, context.Canceled) {
				return nil, fmt.Errorf("hub serve: %w", inst.hubErr)
			}
			return nil, fmt.Errorf("wait for hub local listener: %w", err)
		}
	case <-inst.hubDone:
		cancel()
		if inst.hubErr != nil {
			return nil, fmt.Errorf("hub serve: %w", inst.hubErr)
		}
		return nil, errors.New("hub serve exited before listener became ready")
	}

	// In dev mode the first admin may not exist yet; defer worker bringup
	// until /setup completes.
	statePath := filepath.Join(workerDataDir, "state.json")
	setupWorker := func(ctx context.Context) error {
		return bringUpLocalWorker(ctx, &inst.wg, server, statePath, workerDataDir, hubCfg, listenURL, modeName)
	}
	err = setupWorker(soloCtx)
	switch {
	case err == nil:
		// Worker is up.
	case errors.Is(err, store.ErrNotFound) && cfg.DevMode:
		slog.Info("dev mode: deferring worker auto-registration until first admin signs up via /setup")
		inst.wg.Add(1)
		go pollForDeferredWorkerSetup(soloCtx, &inst.wg, setupWorker)
	default:
		cancel()
		inst.wg.Wait()
		return nil, fmt.Errorf("auto-register worker: %w", err)
	}

	slog.Info("leapmux "+modeName+" listening", "listen", hubCfg.Listen)

	// If the Hub exits unexpectedly, cancel soloCtx so the worker tears down
	// promptly instead of looping against a dead endpoint.
	go func() {
		select {
		case <-inst.hubDone:
			if inst.hubErr != nil {
				slog.Error("hub error", "error", inst.hubErr)
			}
			cancel()
		case <-soloCtx.Done():
		}
	}()

	return inst, nil
}

// soloState persists the auto-registered worker credentials.
type soloState struct {
	WorkerID         string `json:"worker_id"`
	AuthToken        string `json:"auth_token"`
	RegisteredBy     string `json:"registered_by,omitempty"`
	PublicKey        string `json:"public_key,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	MlkemPublicKey   string `json:"mlkem_public_key,omitempty"`
	MlkemPrivateKey  string `json:"mlkem_private_key,omitempty"`
	SlhdsaPublicKey  string `json:"slhdsa_public_key,omitempty"`
	SlhdsaPrivateKey string `json:"slhdsa_private_key,omitempty"`
}

// pollForDeferredWorkerSetup retries setupWorker on a ticker until it succeeds,
// the context is cancelled, or a non-ErrNotFound error occurs. Must be invoked
// as `go pollForDeferredWorkerSetup(...)` with wg.Add(1) already called — this
// function calls wg.Done on exit.
func pollForDeferredWorkerSetup(ctx context.Context, wg *sync.WaitGroup, setupWorker func(context.Context) error) {
	defer wg.Done()
	ticker := time.NewTicker(workerSetupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := setupWorker(ctx)
		if err == nil {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		slog.Error("deferred worker setup failed", "error", err)
		return
	}
}

// bringUpLocalWorker loads (or creates, then persists) the local worker's
// registration state, ensures the composite E2EE keypair is present, and
// launches the worker goroutine under wg. It returns store.ErrNotFound if no
// admin user exists yet; the caller is expected to retry after the first
// /setup signup completes.
func bringUpLocalWorker(
	ctx context.Context,
	wg *sync.WaitGroup,
	server *hub.Server,
	statePath, workerDataDir string,
	hubCfg *hubconfig.Config,
	listenURL, modeName string,
) error {
	state, err := loadOrCreateWorkerState(ctx, server, statePath, workerDataDir)
	if err != nil {
		return err
	}

	// Ensure composite keypair for E2EE.
	if state.PublicKey == "" || state.PrivateKey == "" ||
		state.MlkemPublicKey == "" || state.MlkemPrivateKey == "" ||
		state.SlhdsaPublicKey == "" || state.SlhdsaPrivateKey == "" {
		ck, kpErr := noiseutil.GenerateCompositeKeypair()
		if kpErr != nil {
			return fmt.Errorf("generate composite keypair: %w", kpErr)
		}
		slhdsaPub, _ := ck.SlhdsaPublicKeyBytes()
		slhdsaPriv, _ := ck.SlhdsaPrivateKey.MarshalBinary()
		state.PublicKey = base64.StdEncoding.EncodeToString(ck.X25519Public)
		state.PrivateKey = base64.StdEncoding.EncodeToString(ck.X25519Private)
		state.MlkemPublicKey = base64.StdEncoding.EncodeToString(ck.MlkemPublicKeyBytes())
		state.MlkemPrivateKey = base64.StdEncoding.EncodeToString(ck.MlkemDecapsulationKey.Bytes())
		state.SlhdsaPublicKey = base64.StdEncoding.EncodeToString(slhdsaPub)
		state.SlhdsaPrivateKey = base64.StdEncoding.EncodeToString(slhdsaPriv)
		if err := persistState(statePath, state); err != nil {
			slog.Warn("failed to save keypair", "error", err)
		}
	}

	compositeKey, ckErr := noiseutil.RestoreCompositeKeypair(
		mustDecode64(state.PublicKey),
		mustDecode64(state.PrivateKey),
		mustDecode64(state.MlkemPrivateKey),
		mustDecode64(state.SlhdsaPublicKey),
		mustDecode64(state.SlhdsaPrivateKey),
	)
	if ckErr != nil {
		return fmt.Errorf("restore composite keypair: %w", ckErr)
	}

	slog.Info(modeName+" worker registered",
		"worker_id", state.WorkerID,
		"local", listenURL,
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if wErr := worker.Run(ctx, worker.RunConfig{
			HubURL:       listenURL,
			DataDir:      workerDataDir,
			AuthToken:    state.AuthToken,
			CompositeKey: compositeKey,
			WorkerID:     state.WorkerID,
			DBMaxConns:   hubCfg.Storage.SQLite.MaxConns,
			DBCacheSize:  hubCfg.Storage.SQLite.CacheSize,
			DBMmapSize:   hubCfg.Storage.SQLite.MmapSize,
			// 0 (the default) lets the worker apply channelwire.DefaultMaxIncompleteChunked.
			MaxIncompleteChunked: parseInt(hubCfg.Extras["max_incomplete_chunked"], 0),
			AgentStartupTimeout:  hubCfg.AgentStartupTimeout(),
			APITimeout:           hubCfg.APITimeout(),
			EncryptionMode:       workerconfig.ParseEncryptionMode(hubCfg.Extras["encryption_mode"]),
			UseLoginShell:        parseBool(hubCfg.Extras["use_login_shell"], true),
			RegisteredBy:         state.RegisteredBy,
		}); wErr != nil {
			slog.Error("worker error", "error", wErr)
		}
	}()

	return nil
}

// workerRegistrar is the slice of *hub.Server that worker-state loading needs:
// resolve a worker's owner, find the user to attribute a new local worker to, and
// register one. Depending on the three methods rather than the whole Server lets
// the state-file rules -- which owner wins, when to re-register -- be tested without
// binding listeners and standing up a database.
type workerRegistrar interface {
	GetWorkerOwner(ctx context.Context, workerID string) (string, error)
	GetAdminUser(ctx context.Context) (userID, orgID string, err error)
	RegisterWorker(ctx context.Context, userID string) (*hub.WorkerCredentials, error)
}

func loadOrCreateWorkerState(ctx context.Context, server workerRegistrar, statePath, workerDataDir string) (*soloState, error) {
	if err := os.MkdirAll(workerDataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create worker data dir: %w", err)
	}

	data, err := os.ReadFile(statePath)
	if err == nil {
		var s soloState
		unmarshalErr := json.Unmarshal(data, &s)
		if unmarshalErr == nil && s.WorkerID != "" && s.AuthToken != "" {
			// Take the owner from the DB, which is the authority: workers.registered_by
			// is NOT NULL and set at registration, and it is the fact requireWorkerOwner
			// gates the whole machine on. The state file's copy is a cache that can lag
			// it, and an empty one is not something to paper over -- a worker launched
			// with no owner has every machine-scoped family (file, git, sysinfo, tunnel)
			// permanently dead for its own legitimate user, failing closed in a way that
			// reads exactly like a genuine cross-tenant refusal.
			//
			// This replaces a backfill from GetAdminUser. Sourcing ownership from
			// "whoever the admin is now" answers a different question that merely shares
			// an answer on a fresh single-user install: on any install where the admin
			// changed, it silently reassigned the machine to them.
			owner, dbErr := server.GetWorkerOwner(ctx, s.WorkerID)
			if dbErr == nil {
				if owner == "" {
					// Unreachable via the schema (NOT NULL, set at registration), so this
					// means a hand-edited row or a mint path that dropped it. Re-register
					// rather than launch an ownerless worker.
					slog.Warn("saved worker has no recorded owner, re-registering", "worker_id", s.WorkerID)
				} else {
					if s.RegisteredBy != owner {
						// The file disagreed with the DB (or predates the field). The DB wins;
						// refresh the cache so the next launch matches without a second query.
						s.RegisteredBy = owner
						if err := persistState(statePath, &s); err != nil {
							// The in-memory correction still applies; a failed write only
							// costs the cache and forces this DB lookup again next launch.
							slog.Warn("failed to refresh worker state cache", "error", err)
						}
					}
					return &s, nil
				}
			} else if errors.Is(dbErr, store.ErrNotFound) {
				// The worker row is genuinely gone (deleted, or never persisted):
				// re-register a fresh identity below.
				slog.Warn("saved worker not found in DB, re-registering", "worker_id", s.WorkerID)
			} else {
				// A transient store failure (e.g. sqlite "database is locked"
				// racing another writer at startup) is NOT a deletion. Treating it
				// as one would discard the saved WorkerID and re-register a brand-new
				// identity, orphaning every workspace and tab still pointed at the old
				// worker. Fail the launch so a retry can find the row intact.
				return nil, fmt.Errorf("look up saved worker %q owner: %w", s.WorkerID, dbErr)
			}
		} else {
			// The file exists but is not a usable worker state -- a partial write
			// (power loss or OS crash mid-write, before this launch made writes
			// atomic), a hand-edit, or a truncated tail. The saved identity is
			// unrecoverable from these bytes, so re-registering a fresh worker
			// below is the only forward path, but it ORPHANS every workspace and
			// tab still pointing at the old worker_id -- the same loss the
			// transient-DB-error guard above exists to prevent. The DB analogue
			// fails the launch to keep the saved identity; here the identity is
			// already gone, so the best we can do is fail LOUDLY (Error, not a
			// silent fall-through) and preserve the corrupt file so an operator
			// can re-link the orphaned workspaces before the reaper collects them.
			if unmarshalErr != nil {
				slog.Error("saved worker state is unreadable; re-registering a fresh worker, which orphans the previous one",
					"state_path", statePath, "err", unmarshalErr)
			} else {
				slog.Error("saved worker state is missing its worker id or auth token; re-registering a fresh worker, which orphans the previous one",
					"state_path", statePath)
			}
			preserveCorruptState(statePath, data)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read state: %w", err)
	}

	userID, _, err := server.GetAdminUser(ctx)
	if err != nil {
		return nil, err
	}

	creds, err := server.RegisterWorker(ctx, userID)
	if err != nil {
		return nil, err
	}

	state := &soloState{
		WorkerID:     creds.WorkerID,
		AuthToken:    creds.AuthToken,
		RegisteredBy: userID,
	}

	if err := persistState(statePath, state); err != nil {
		return nil, err
	}

	return state, nil
}

// persistState writes the worker state file with the one encoding and mode
// (pretty JSON, 0o600) every writer must agree on. Callers keep their own
// error posture -- the keypair backfill and the RegisteredBy cache refresh log
// and continue, the initial registration fails the launch -- but the file's
// format contract lives here so a change cannot silently miss a write site.
//
// The write is atomic (temp file in the same directory, then rename) so a
// crash or power loss mid-write can never leave a half-written file behind.
// loadOrCreateWorkerState treats such a file as a lost identity and
// re-registers a fresh worker, orphaning every workspace still pointed at the
// old one -- the same loss a transient-DB-error at startup would cause if it
// were misread as a deletion -- so the write that produces the file the next
// launch reads must not be able to corrupt it.
func persistState(statePath string, s *soloState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	dir := filepath.Dir(statePath)
	tmp, err := os.CreateTemp(dir, ".worker-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, statePath); err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	removeTmp = false
	return nil
}

// preserveCorruptState renames an unreadable worker-state file aside so a
// later launch does not re-log the same orphaning and an operator can inspect
// or re-link it. Best-effort: a failure leaves the bytes in place for the next
// launch to warn about again, which is still better than silently overwriting.
func preserveCorruptState(statePath string, data []byte) {
	sidecar := fmt.Sprintf("%s.corrupt-%d", statePath, time.Now().UnixNano())
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		slog.Warn("could not preserve corrupt worker state for forensics", "sidecar", sidecar, "err", err)
	}
}

func mustDecode64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// shouldWarnNonLoopback reports whether solo.Start should emit the
// non-loopback security warning. Dev mode uses real password auth so it is
// exempt; NoTCP means there is no TCP listener to warn about.
func shouldWarnNonLoopback(cfg Config, listen string) bool {
	return !cfg.DevMode && !cfg.NoTCP && listenIsNonLoopback(listen)
}

// listenIsNonLoopback reports whether `listen` would expose the hub on
// something other than a loopback address. Used only to drive the solo-mode
// security warning; the heuristic stays conservative — empty/missing host or
// an unparseable hostname is treated as non-loopback so the warning errs on
// the side of being shown. Wildcard addresses ("0.0.0.0", "::") parse as
// non-loopback IPs, so `net.IP.IsLoopback` already handles them.
func listenIsNonLoopback(listen string) bool {
	if listen == "" {
		return true
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// defaultExtraFlags are the koanf-backed flags solo/dev registers on top of the
// hub's own flag table. Every one of them is a WORKER-scoped setting: solo embeds
// a Worker, but the hub config file is the only config file solo reads, so these
// are the sole way to tune the embedded Worker. They are "extras" precisely
// because they are not hub settings and must not appear on `leapmux hub`.
//
// max-incomplete-chunked is here rather than in the hub's flag table because the
// Hub has no chunk-count cap to tune: channelmgr's interleaving guard admits only
// ONE in-flight chunked sequence per channel+direction, which is strictly stronger
// than any count cap. The cap is real on the WORKER (channel.NewManager -> the
// reassembly budget in session.go), which is what this forwards to. Do not "restore"
// it as a hub flag -- it would be dead there.
func defaultExtraFlags() []hubconfig.ExtraFlagDef {
	return []hubconfig.ExtraFlagDef{
		{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
		{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap claude invocation in user's login shell", StrDefault: "true"},
		{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", Usage: "maximum in-flight chunked sequences per channel for the embedded worker (default 4)", StrDefault: "0", Category: "Timeout and limit options"},
	}
}

// parseInt parses a string as an int, returning defaultVal if the string is
// empty or not a valid integer. It is the int counterpart of parseBool, used to
// read the string-typed Extras map that carries solo's worker-scoped flags.
func parseInt(s string, defaultVal int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return defaultVal
	}
	return n
}

// parseBool parses a string as a boolean, returning defaultVal if the string
// is empty or not recognized.
func parseBool(s string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return defaultVal
	}
}
