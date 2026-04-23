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
	"os"
	"path/filepath"
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
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/worker"
)

// workerSetupPollInterval is how often dev mode re-checks whether the first
// admin user has completed the /setup flow so the auto-registered local
// worker can come online.
const workerSetupPollInterval = 2 * time.Second

// Config configures the solo launcher.
type Config struct {
	// Addr is the listen address (default: "127.0.0.1:4327").
	Addr string
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
	server    *hub.Server
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

// Server returns the underlying Hub server instance.
func (i *Instance) Server() *hub.Server {
	return i.server
}

// Wait blocks until the Hub exits (either via Stop or because it failed
// on its own) and returns its terminal error. Returns nil on clean
// shutdown or http.ErrServerClosed. Safe to call multiple times.
func (i *Instance) Wait() error {
	<-i.hubDone
	return i.hubErr
}

// Stop gracefully shuts down the Hub and Worker.
func (i *Instance) Stop() {
	i.cancel()
	i.wg.Wait()
}

// Start launches a Hub and Worker in-process. It returns an Instance that
// can be used to stop the services. The caller should defer inst.Stop().
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	logging.Setup()

	modeName := "solo"
	if cfg.DevMode {
		modeName = "dev"
	}

	defaultAddr := cfg.Addr
	if defaultAddr == "" {
		defaultAddr = "127.0.0.1:4327"
		if cfg.DevMode {
			defaultAddr = ":4327"
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
		cliFlags = []string{"addr", "data-dir", "dev-frontend", "storage-sqlite-max-conns", "storage-sqlite-cache-size", "storage-sqlite-mmap-size", "max-message-size", "max-incomplete-chunked", "api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds", "log-level", "use-login-shell"}
	}

	extraFlags := cfg.ExtraFlags
	if extraFlags == nil {
		extraFlags = []hubconfig.ExtraFlagDef{
			{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
			{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap claude invocation in user's login shell", StrDefault: "true"},
		}
	}

	hubCfg, _, err := hubconfig.LoadWithOptions(cfg.Args, hubconfig.LoadOptions{
		DefaultAddr:       defaultAddr,
		DefaultConfigDir:  configDir,
		DefaultConfigFile: configFile,
		FlagSetName:       "leapmux",
		CLIFlags:          cliFlags,
		ExtraFlags:        extraFlags,
		SoloMode:          !cfg.DevMode,
	})
	if err != nil {
		return nil, fmt.Errorf("load hub config: %w", err)
	}
	hubCfg.DevMode = cfg.DevMode

	level, err := logging.ParseLevel(hubCfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	if cfg.NoTCP {
		hubCfg.Addr = ""
	}

	if !cfg.SkipBanner {
		logging.PrintBanner(modeName, logging.VersionInfo{Version: version.Value, CommitHash: version.CommitHash, CommitTime: version.CommitTime, BuildTime: version.BuildTime})
		logging.PrintAccessURL(hubCfg.Addr)
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
		server:    server,
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

	slog.Info("leapmux "+modeName+" listening", "addr", hubCfg.Addr)

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
		stateData, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			slog.Warn("failed to marshal state", "error", err)
		} else if writeErr := os.WriteFile(statePath, stateData, 0o600); writeErr != nil {
			slog.Warn("failed to save keypair", "error", writeErr)
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
			HubURL:               listenURL,
			DataDir:              workerDataDir,
			AuthToken:            state.AuthToken,
			CompositeKey:         compositeKey,
			WorkerID:             state.WorkerID,
			DBMaxConns:           hubCfg.Storage.SQLite.MaxConns,
			DBCacheSize:          hubCfg.Storage.SQLite.CacheSize,
			DBMmapSize:           hubCfg.Storage.SQLite.MmapSize,
			MaxMessageSize:       hubCfg.MaxMessageSize,
			MaxIncompleteChunked: hubCfg.MaxIncompleteChunked,
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

func loadOrCreateWorkerState(ctx context.Context, server *hub.Server, statePath, workerDataDir string) (*soloState, error) {
	if err := os.MkdirAll(workerDataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create worker data dir: %w", err)
	}

	data, err := os.ReadFile(statePath)
	if err == nil {
		var s soloState
		if json.Unmarshal(data, &s) == nil && s.WorkerID != "" && s.AuthToken != "" {
			if dbErr := server.GetWorkerByID(ctx, s.WorkerID); dbErr == nil {
				// Backfill RegisteredBy for state files created before this field existed.
				if s.RegisteredBy == "" {
					if adminID, _, aErr := server.GetAdminUser(ctx); aErr == nil {
						s.RegisteredBy = adminID
						if updated, mErr := json.MarshalIndent(s, "", "  "); mErr == nil {
							_ = os.WriteFile(statePath, updated, 0o600)
						}
					}
				}
				return &s, nil
			}
			slog.Warn("saved worker not found in DB, re-registering")
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

	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(statePath, stateData, 0o600); err != nil {
		return nil, fmt.Errorf("write state: %w", err)
	}

	return state, nil
}

func mustDecode64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
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
