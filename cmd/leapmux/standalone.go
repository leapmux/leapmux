package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/leapmux/leapmux/hub"
	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubdb "github.com/leapmux/leapmux/internal/hub/db"
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/worker"
)

// standaloneState persists the auto-registered worker credentials.
type standaloneState struct {
	WorkerID   string `json:"worker_id"`
	AuthToken  string `json:"auth_token"`
	PublicKey  string `json:"public_key,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

func runStandalone(args []string) error {
	// Define CLI flags.
	fs := flag.NewFlagSet("leapmux", flag.ContinueOnError)
	fs.String("addr", ":4327", "TCP listen address")
	fs.String("data-dir", defaultStandaloneDataDir(), "data directory")
	fs.String("dev-frontend", "", "Vite dev server URL (dev mode)")
	fs.Int("db-max-conns", hubdb.DefaultMaxConns, "maximum number of open database connections")
	fs.String("log-level", "info", "log level (debug, info, warn, error)")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	// Flag name -> koanf key mapping.
	fieldMap := map[string]string{
		"addr":         "addr",
		"data-dir":     "data_dir",
		"dev-frontend": "dev_frontend",
		"db-max-conns": "db_max_conns",
		"log-level":    "log_level",
	}

	defaults := map[string]interface{}{
		"addr":         ":4327",
		"data_dir":     defaultStandaloneDataDir(),
		"dev_frontend": "",
		"db_max_conns": hubdb.DefaultMaxConns,
		"log_level":    "info",
	}

	k := koanf.New(".")
	fp := internalconfig.NewFlagProvider(fs, fieldMap)

	if err := internalconfig.Load(k, defaults, "", "LEAPMUX_HUB_", fp); err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	addr := k.String("addr")
	dataDir := k.String("data_dir")
	devFrontend := k.String("dev_frontend")
	dbMaxConns := k.Int("db_max_conns")
	logLevel := k.String("log_level")

	// Expand ~ in data dir.
	dataDir = internalconfig.ExpandHome(dataDir)

	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner("standalone", version, addr)
	logging.PrintAccessURL(addr)

	// Ensure top-level data directory exists.
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	hubDataDir := filepath.Join(dataDir, "hub")
	workerDataDir := filepath.Join(dataDir, "worker")

	// Load hub config file if it exists at {data-dir}/hub/hub.yaml.
	hubConfigPath := filepath.Join(hubDataDir, "hub.yaml")
	hubK := koanf.New(".")
	hubDefaults := map[string]interface{}{
		"max_message_size":       0,
		"max_incomplete_chunked": 0,
	}
	_ = hubK.Load(confmap.Provider(hubDefaults, "."), nil)
	if _, statErr := os.Stat(hubConfigPath); statErr == nil {
		_ = hubK.Load(file.Provider(hubConfigPath), yaml.Parser())
	}

	maxMessageSize := hubK.Int("max_message_size")
	maxIncompleteChunked := hubK.Int("max_incomplete_chunked")

	// Start the Hub server.
	server, err := hub.NewServer(hub.ServerConfig{
		DataDir:              hubDataDir,
		Addr:                 addr,
		DevFrontend:          devFrontend,
		DBMaxConns:           dbMaxConns,
		MaxMessageSize:       maxMessageSize,
		MaxIncompleteChunked: maxIncompleteChunked,
	})
	if err != nil {
		return fmt.Errorf("create hub server: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Run Hub in background.
	var wg sync.WaitGroup
	hubErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		hubErrCh <- server.Serve(ctx)
	}()

	// Wait briefly for Hub to start listening on the Unix socket.
	socketPath := server.SocketPath()
	if err := waitForSocket(ctx, socketPath); err != nil {
		stop()
		wg.Wait()
		return fmt.Errorf("wait for hub socket: %w", err)
	}

	// Auto-register worker via direct DB access (no network round-trip).
	statePath := filepath.Join(workerDataDir, "state.json")
	state, err := loadOrCreateWorkerState(ctx, server, statePath, workerDataDir)
	if err != nil {
		stop()
		wg.Wait()
		return fmt.Errorf("auto-register worker: %w", err)
	}

	// Ensure the worker has an X25519 keypair for E2EE channels.
	if state.PublicKey == "" || state.PrivateKey == "" {
		kp, kpErr := noiseutil.GenerateKeypair()
		if kpErr != nil {
			stop()
			wg.Wait()
			return fmt.Errorf("generate keypair: %w", kpErr)
		}
		state.PublicKey = base64.StdEncoding.EncodeToString(kp.Public)
		state.PrivateKey = base64.StdEncoding.EncodeToString(kp.Private)
		stateData, _ := json.MarshalIndent(state, "", "  ")
		if writeErr := os.WriteFile(statePath, stateData, 0o600); writeErr != nil {
			slog.Warn("failed to save keypair", "error", writeErr)
		}
	}

	privateKey, _ := base64.StdEncoding.DecodeString(state.PrivateKey)
	publicKey, _ := base64.StdEncoding.DecodeString(state.PublicKey)

	slog.Info("standalone worker registered",
		"worker_id", state.WorkerID,
		"socket", socketPath,
	)

	// Run Worker in background, connecting via the Hub's Unix domain socket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := worker.Run(ctx, worker.RunConfig{
			HubURL:     "unix:" + socketPath,
			DataDir:    workerDataDir,
			AuthToken:  state.AuthToken,
			PrivateKey: privateKey,
			PublicKey:  publicKey,
			WorkerID:   state.WorkerID,
			DBMaxConns: dbMaxConns,
		}); err != nil {
			slog.Error("worker error", "error", err)
		}
	}()

	slog.Info("leapmux standalone listening", "addr", addr)

	// Wait for Hub error or context cancellation.
	select {
	case err := <-hubErrCh:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
		wg.Wait()
		return nil
	}
}

// waitForSocket polls until the Unix socket exists (max ~5 seconds).
func waitForSocket(ctx context.Context, path string) error {
	for i := 0; i < 50; i++ {
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

// loadOrCreateWorkerState loads saved credentials or creates a new worker
// record directly in the Hub's database (avoiding the registration flow).
func loadOrCreateWorkerState(ctx context.Context, server *hub.Server, statePath, workerDataDir string) (*standaloneState, error) {
	if err := os.MkdirAll(workerDataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create worker data dir: %w", err)
	}

	// Try loading existing state.
	data, err := os.ReadFile(statePath)
	if err == nil {
		var s standaloneState
		if json.Unmarshal(data, &s) == nil && s.WorkerID != "" && s.AuthToken != "" {
			// Verify the worker still exists in the DB.
			if dbErr := server.GetWorkerByID(ctx, s.WorkerID); dbErr == nil {
				return &s, nil
			}
			slog.Warn("saved worker not found in DB, re-registering")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read state: %w", err)
	}

	// Find the admin user (created by bootstrap).
	userID, orgID, err := server.GetAdminUser(ctx)
	if err != nil {
		return nil, err
	}

	creds, err := server.RegisterWorker(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}

	state := &standaloneState{
		WorkerID:  creds.WorkerID,
		AuthToken: creds.AuthToken,
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

func defaultStandaloneDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "leapmux")
	}
	return filepath.Join(home, ".config", "leapmux")
}
