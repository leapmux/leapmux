package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/logging"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/solo"
	"github.com/leapmux/leapmux/worker"
)

// The foreground implementations the lifecycle `run` verb dispatches
// to. Each takes the COMMON runtime's root lease through TakeLease --
// the transfer the plan's ownership contract states -- and the mode's
// own flags.

// runHubForeground runs the Hub under the started runtime.
func runHubForeground(runtime *lifecycle.Runtime, args []string) error {
	cfg, _, err := hubconfig.Load(args)
	if err != nil {
		return err
	}
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	// The runtime's root lease becomes the hub's lease here.
	server, err := hub.NewServer(cfg, runtime.TakeLease())
	if err != nil {
		return err
	}
	server.PrintBannerURL()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-runtime.StopSignal():
			stop()
		}
	}()
	// Ready closes INSIDE Serve; watch it concurrently and report the
	// runtime's readiness as the Hub's initialization completes.
	go func() {
		<-server.Ready()
		runtime.SetReadiness(lifecycle.ReadinessReady)
	}()
	return server.Serve(ctx)
}

// runWorkerForeground runs the worker under the started runtime. The
// saved registration is the only credential source; registration lives
// in `leapmux worker register`.
func runWorkerForeground(runtime *lifecycle.Runtime, args []string) error {
	cfg, _, err := workerconfig.Load(args)
	if err != nil {
		return err
	}
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	// The runtime's root lease transfers into worker.Run, which
	// releases it after the database closes.
	lease := runtime.TakeLease()
	if lease.Mode() != lifecycle.RoleWorker {
		// The runtime acquired the directory under the worker role by
		// construction; this guards a future mode mix-up.
		_ = lease.Close()
		return errors.New("the runtime's lease is not the worker component's")
	}

	state, err := cfg.UsableRegistration()
	if err != nil {
		return err
	}
	compositeKey, err := state.CompositeKeypair()
	if err != nil {
		return err
	}

	runtime.SetRegistration(lifecycle.RegistrationStatus{
		State:    lifecycle.RegistrationRegistered,
		WorkerID: state.WorkerID,
		HubURL:   state.HubURL,
	})
	runtime.SetReadiness(lifecycle.ReadinessStarting)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
			// A signal ends the context; name it for the log the way
			// the standalone command always did.
			if ctx.Err() != nil {
				log.Printf("shutdown signal received")
			}
		case <-runtime.StopSignal():
			stop()
		}
	}()

	err = worker.Run(ctx, worker.RunConfig{
		HubURL:    state.HubURL,
		Directory: lease,
		OnReady: func() {
			runtime.SetReadiness(lifecycle.ReadinessReady)
		},
		OnConnectionState: func(state worker.ConnectionState) {
			kind := lifecycle.ConnectionConnected
			lastErr := ""
			if !state.Connected {
				kind = lifecycle.ConnectionDisconnected
				if state.Err != nil {
					lastErr = state.Err.Error()
				}
			}
			runtime.SetConnection(lifecycle.ConnectionStatus{State: kind, LastError: lastErr})
		},
		AuthToken:    state.AuthToken,
		CompositeKey: compositeKey,
		WorkerID:     state.WorkerID,
		Name:         cfg.Name,

		DBMaxConns:              cfg.DBMaxConns,
		DBCacheSize:             cfg.DBCacheSize,
		DBMmapSize:              cfg.DBMmapSize,
		MaxIncompleteChunked:    cfg.MaxIncompleteChunked,
		MaxMessageSize:          cfg.MaxMessageSize,
		AgentStartupTimeout:     cfg.AgentStartupTimeout,
		APITimeout:              cfg.APITimeout,
		AgentStartupConcurrency: cfg.AgentStartupConcurrency,
		EncryptionMode:          cfg.EncryptionModeProto(),
		UseLoginShell:           cfg.UseLoginShell,
		OnDeregister: func() {
			if err := clearDeletedWorkerState(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "failed to clear the worker state: %v\n", err)
			}
		},
	})
	if err != nil {
		runtime.SetProcessState(lifecycle.ProcessFailed)
	}
	return reregistrationAdvice(err, cfg.StatePath())
}

// runSoloForeground runs the combined instance under the started
// runtime: the prepared entry receives the runtime's root lease.
func runSoloForeground(devMode bool, runtime *lifecycle.Runtime, args []string) error {
	mode := lifecycle.RoleSolo
	if devMode {
		mode = lifecycle.RoleDev
	}
	cfg := solo.Config{Args: args, SkipBanner: false, DevMode: devMode}

	prepared, err := solo.Prepare(cfg)
	if err != nil {
		return err
	}
	if prepared.Role() != mode {
		return fmt.Errorf("the prepared launch runs as %s, not %s", prepared.Role(), mode)
	}
	// The runtime's root lease IS the combined root lease: transfer it
	// into the prepared startup, which owns it through the shutdown.
	root := runtime.TakeLease()
	if root.Mode() != mode || root.Path() != prepared.DataDirectory() {
		_ = root.Close()
		return fmt.Errorf("the runtime's lease does not match the prepared %s launch", mode)
	}

	inst, err := solo.StartPrepared(context.Background(), prepared, root)
	if err != nil {
		return err
	}
	go func() {
		select {
		case <-runtime.StopSignal():
			_ = inst.Stop()
		case <-inst.WaitDone():
		}
	}()
	snap := inst.Startup()
	if snap.WorkerPendingSetup {
		runtime.SetReadiness(lifecycle.ReadinessReadyForSetup)
	} else if snap.WorkerReady {
		runtime.SetReadiness(lifecycle.ReadinessReady)
	}
	if err := inst.Wait(); err != nil {
		return err
	}
	return inst.Stop()
}
