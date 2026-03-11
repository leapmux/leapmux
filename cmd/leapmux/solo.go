package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/worker"
)

// soloState persists the auto-registered worker credentials.
type soloState struct {
	WorkerID         string `json:"worker_id"`
	AuthToken        string `json:"auth_token"`
	PublicKey        string `json:"public_key,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	MlkemPublicKey   string `json:"mlkem_public_key,omitempty"`
	MlkemPrivateKey  string `json:"mlkem_private_key,omitempty"`
	SlhdsaPublicKey  string `json:"slhdsa_public_key,omitempty"`
	SlhdsaPrivateKey string `json:"slhdsa_private_key,omitempty"`
}

func runSolo(args []string, soloMode bool) error {
	modeName := "solo"
	if !soloMode {
		modeName = "dev"
	}

	defaultAddr := "127.0.0.1:4327"
	if !soloMode {
		defaultAddr = ":4327"
	}

	defaultConfigDir := "~/.config/leapmux/" + modeName
	defaultConfigFile := defaultConfigDir + "/" + modeName + ".yaml"

	hubCfg, showVersion, err := hubconfig.LoadWithOptions(args, hubconfig.LoadOptions{
		DefaultAddr:       defaultAddr,
		DefaultConfigDir:  defaultConfigDir,
		DefaultConfigFile: defaultConfigFile,
		FlagSetName:       "leapmux",
		CLIFlags:          []string{"addr", "data-dir", "dev-frontend", "db-max-conns", "log-level"},
		SoloMode:          soloMode,
	})
	if err != nil {
		return err
	}

	if showVersion {
		fmt.Println(version)
		return nil
	}

	level, err := logging.ParseLevel(hubCfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	logging.PrintBanner(modeName, version, hubCfg.Addr)
	logging.PrintAccessURL(modeName, hubCfg.Addr)

	// Split the data dir into hub and worker subdirectories.
	dataDir := hubCfg.DataDir
	hubCfg.DataDir = filepath.Join(dataDir, "hub")
	workerDataDir := filepath.Join(dataDir, "worker")

	// Ensure top-level data directory exists.
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// Start the Hub server.
	server, err := hub.NewServer(hubCfg)
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

	// Ensure the worker has a composite keypair for E2EE channels.
	if state.PublicKey == "" || state.PrivateKey == "" || state.MlkemPublicKey == "" || state.MlkemPrivateKey == "" || state.SlhdsaPublicKey == "" || state.SlhdsaPrivateKey == "" {
		ck, kpErr := noiseutil.GenerateCompositeKeypair()
		if kpErr != nil {
			stop()
			wg.Wait()
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
		stateData, _ := json.MarshalIndent(state, "", "  ")
		if writeErr := os.WriteFile(statePath, stateData, 0o600); writeErr != nil {
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
		stop()
		wg.Wait()
		return fmt.Errorf("restore composite keypair: %w", ckErr)
	}

	slog.Info(modeName+" worker registered",
		"worker_id", state.WorkerID,
		"socket", socketPath,
	)

	// Run Worker in background, connecting via the Hub's Unix domain socket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := worker.Run(ctx, worker.RunConfig{
			HubURL:              "unix:" + socketPath,
			DataDir:             workerDataDir,
			AuthToken:           state.AuthToken,
			CompositeKey:        compositeKey,
			WorkerID:            state.WorkerID,
			Version:             version,
			DBMaxConns:          hubCfg.DBMaxConns,
			AgentStartupTimeout: hubCfg.AgentStartupTimeout(),
		}); err != nil {
			slog.Error("worker error", "error", err)
		}
	}()

	slog.Info("leapmux "+modeName+" listening", "addr", hubCfg.Addr)

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
func loadOrCreateWorkerState(ctx context.Context, server *hub.Server, statePath, workerDataDir string) (*soloState, error) {
	if err := os.MkdirAll(workerDataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create worker data dir: %w", err)
	}

	// Try loading existing state.
	data, err := os.ReadFile(statePath)
	if err == nil {
		var s soloState
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

	state := &soloState{
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

func mustDecode64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}
