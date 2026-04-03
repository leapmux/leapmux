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
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/util/version"
	"github.com/leapmux/leapmux/worker"
)

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
	cancel context.CancelFunc
	wg     sync.WaitGroup
	addr   string
	server *hub.Server
}

// Addr returns the listen address of the running instance.
func (i *Instance) Addr() string {
	return i.addr
}

// Server returns the underlying Hub server instance.
func (i *Instance) Server() *hub.Server {
	return i.server
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
		cliFlags = []string{"addr", "data-dir", "dev-frontend", "db-max-conns", "max-message-size", "max-incomplete-chunked", "api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds", "log-level", "use-login-shell"}
	}

	hubCfg, _, err := hubconfig.LoadWithOptions(cfg.Args, hubconfig.LoadOptions{
		DefaultAddr:       defaultAddr,
		DefaultConfigDir:  configDir,
		DefaultConfigFile: configFile,
		FlagSetName:       "leapmux",
		CLIFlags:          cliFlags,
		ExtraFlags: []hubconfig.ExtraFlagDef{
			{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
			{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap claude invocation in user's login shell", StrDefault: "true"},
		},
		SoloMode: !cfg.DevMode,
	})
	if err != nil {
		return nil, fmt.Errorf("load hub config: %w", err)
	}

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
		logging.PrintAccessURL(modeName, hubCfg.Addr)
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

	inst := &Instance{cancel: cancel, addr: hubCfg.Addr, server: server}

	// Start Hub.
	hubErrCh := make(chan error, 1)
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		hubErrCh <- server.Serve(soloCtx)
	}()

	// Wait for Hub's Unix socket.
	socketPath := server.SocketPath()
	if err := waitForSocket(soloCtx, socketPath); err != nil {
		cancel()
		inst.wg.Wait()
		return nil, fmt.Errorf("wait for hub socket: %w", err)
	}

	// Auto-register worker.
	statePath := filepath.Join(workerDataDir, "state.json")
	state, err := loadOrCreateWorkerState(soloCtx, server, statePath, workerDataDir)
	if err != nil {
		cancel()
		inst.wg.Wait()
		return nil, fmt.Errorf("auto-register worker: %w", err)
	}

	// Ensure composite keypair for E2EE.
	if state.PublicKey == "" || state.PrivateKey == "" ||
		state.MlkemPublicKey == "" || state.MlkemPrivateKey == "" ||
		state.SlhdsaPublicKey == "" || state.SlhdsaPrivateKey == "" {
		ck, kpErr := noiseutil.GenerateCompositeKeypair()
		if kpErr != nil {
			cancel()
			inst.wg.Wait()
			return nil, fmt.Errorf("generate composite keypair: %w", kpErr)
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
		cancel()
		inst.wg.Wait()
		return nil, fmt.Errorf("restore composite keypair: %w", ckErr)
	}

	slog.Info(modeName+" worker registered",
		"worker_id", state.WorkerID,
		"socket", socketPath,
	)

	// Start Worker.
	inst.wg.Add(1)
	go func() {
		defer inst.wg.Done()
		if wErr := worker.Run(soloCtx, worker.RunConfig{
			HubURL:               "unix:" + socketPath,
			DataDir:              workerDataDir,
			AuthToken:            state.AuthToken,
			CompositeKey:         compositeKey,
			WorkerID:             state.WorkerID,
			DBMaxConns:           hubCfg.DBMaxConns,
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

	slog.Info("leapmux "+modeName+" listening", "addr", hubCfg.Addr)

	// Monitor Hub in background.
	go func() {
		select {
		case err := <-hubErrCh:
			if err != nil {
				slog.Error("hub error", "error", err)
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

func waitForSocket(ctx context.Context, path string) error {
	for range 50 {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("socket %s not created in time", path)
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

	userID, orgID, err := server.GetAdminUser(ctx)
	if err != nil {
		return nil, err
	}

	creds, err := server.RegisterWorker(ctx, orgID, userID)
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
