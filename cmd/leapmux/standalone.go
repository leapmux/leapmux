package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/leapmux/leapmux/hub"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/worker"
	"golang.org/x/net/http2"
)

// standaloneState persists the auto-registered worker credentials.
type standaloneState struct {
	WorkerID  string `json:"worker_id"`
	AuthToken string `json:"auth_token"`
}

func runStandalone(args []string) error {
	fs := flag.NewFlagSet("leapmux", flag.ExitOnError)
	addr := fs.String("addr", ":4327", "TCP listen address")
	dataDir := fs.String("data-dir", defaultStandaloneDataDir(), "data directory")
	devFrontend := fs.String("dev-frontend", "", "Vite dev server URL (dev mode)")
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	logging.PrintBanner("standalone", version, *addr)

	// Ensure top-level data directory exists.
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	hubDataDir := filepath.Join(*dataDir, "hub")
	workerDataDir := filepath.Join(*dataDir, "worker")

	// Start the Hub server.
	server, err := hub.NewServer(hub.ServerConfig{
		DataDir:     hubDataDir,
		Addr:        *addr,
		DevFrontend: *devFrontend,
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

	slog.Info("standalone worker registered",
		"worker_id", state.WorkerID,
		"socket", socketPath,
	)

	// Create an h2c HTTP client that dials via Unix socket.
	unixClient := &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	// Run Worker in background.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Use a dummy URL — the Unix socket transport ignores the host.
		if err := worker.Run(ctx, worker.RunConfig{
			HubURL:     "http://localhost",
			DataDir:    workerDataDir,
			AuthToken:  state.AuthToken,
			HTTPClient: unixClient,
		}); err != nil {
			slog.Error("worker error", "error", err)
		}
	}()

	slog.Info("leapmux standalone listening", "addr", *addr)

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

	hostname, _ := os.Hostname()

	creds, err := server.RegisterWorker(ctx, orgID, "Local", hostname, runtime.GOOS, runtime.GOARCH, userID)
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
