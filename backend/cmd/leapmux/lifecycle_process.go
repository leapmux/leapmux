package main

import (
	"context"
	"errors"
	"fmt"

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
// prepared configuration.

// runHubForeground runs the Hub under the started runtime.
func runHubForeground(ctx context.Context, runtime *lifecycle.Runtime, cfg *hubconfig.Config) error {
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

	// Ready closes INSIDE Serve; watch it concurrently and report the
	// runtime's readiness as the Hub's initialization completes.
	readyCtx, cancelReady := context.WithCancel(ctx)
	readyDone := make(chan struct{})
	go func() {
		defer close(readyDone)
		select {
		case <-server.Ready():
			runtime.SetReadiness(lifecycle.ReadinessReady)
		case <-readyCtx.Done():
		}
	}()
	err = server.Serve(ctx)
	cancelReady()
	<-readyDone
	return err
}

// runWorkerForeground runs the worker under the started runtime. The
// saved registration is the only credential source; registration lives
// in `leapmux worker register`.
func runWorkerForeground(ctx context.Context, runtime *lifecycle.Runtime, cfg *workerconfig.Config) (result error) {
	if err := cfg.ValidateRuntime(); err != nil {
		return fmt.Errorf("validate runtime configuration: %w", err)
	}
	level, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	// The runtime's root lease transfers into worker.Run, which
	// releases it after the database closes.
	lease := runtime.TakeLease()
	defer func() {
		if err := lease.Close(); err != nil && !errors.Is(result, err) {
			result = errors.Join(result, err)
		}
	}()
	if lease == nil || lease.Mode() != lifecycle.RoleWorker {
		// The runtime acquired the directory under the worker role by
		// construction; this guards a future mode mix-up.
		return errors.New("the runtime's lease is not the worker component's")
	}
	canonical, err := lifecycle.CanonicalDir(cfg.DataDir)
	if err != nil {
		return err
	}
	if canonical != lease.Path() {
		return fmt.Errorf("the worker configuration does not match its held data directory")
	}

	state, err := cfg.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	compositeKey, err := state.UsableKeypair()
	if err != nil {
		return err
	}

	runtime.SetRegistration(lifecycle.RegistrationStatus{
		State:    lifecycle.RegistrationRegistered,
		WorkerID: new(state.WorkerID),
		HubURL:   new(state.HubURL),
	})
	runtime.SetReadiness(lifecycle.ReadinessStarting)
	runtime.SetConnection(lifecycle.ConnectionStatus{State: lifecycle.ConnectionConnecting})

	err = worker.Run(ctx, worker.RunConfig{
		HubURL:    state.HubURL,
		Directory: lease,
		OnReady: func() {
			runtime.SetReadiness(lifecycle.ReadinessReady)
		},
		OnConnectionState: func(state worker.ConnectionState) {
			kind := lifecycle.ConnectionConnected
			var lastErr *string
			if !state.Connected {
				kind = lifecycle.ConnectionDisconnected
				if state.Err != nil {
					lastErr = new(state.Err.Error())
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
		OnDeregister:            func() error { return clearDeletedWorkerState(cfg) },
	})
	return reregistrationAdvice(err, cfg.StatePath())
}

// runSoloForeground runs the combined instance under the started
// runtime: the prepared entry receives the runtime's root lease.
func runSoloForeground(ctx context.Context, runtime *lifecycle.Runtime, prepared *solo.PreparedConfig) error {
	mode := prepared.Role()
	// The runtime's root lease IS the combined root lease: transfer it
	// into the prepared startup, which owns it through the shutdown.
	root := runtime.TakeLease()
	if root == nil || root.Mode() != mode || root.Path() != prepared.DataDirectory() {
		return errors.Join(fmt.Errorf("the runtime's lease does not match the prepared %s launch", mode), root.Close())
	}

	runtime.SetRegistration(lifecycle.RegistrationStatus{State: lifecycle.RegistrationPending})
	runtime.SetConnection(lifecycle.ConnectionStatus{State: lifecycle.ConnectionConnecting})
	inst, err := solo.StartPrepared(ctx, prepared, root)
	if err != nil {
		return err
	}
	return waitCombinedForeground(ctx, runtime, inst)
}

type combinedForegroundInstance interface {
	ObserveStartup() (solo.StartupSnapshot, <-chan struct{})
	Startup() solo.StartupSnapshot
	WaitDone() <-chan struct{}
	Stop() error
}

func waitCombinedForeground(ctx context.Context, runtime *lifecycle.Runtime, inst combinedForegroundInstance) error {
	for {
		snapshot, changed := inst.ObserveStartup()
		reportCombinedStartup(runtime, snapshot)
		select {
		case <-changed:
			continue
		case <-ctx.Done():
		case <-inst.WaitDone():
		}
		err := inst.Stop()
		reportCombinedStartup(runtime, inst.Startup())
		return err
	}
}

func reportCombinedStartup(runtime *lifecycle.Runtime, snapshot solo.StartupSnapshot) {
	switch {
	case snapshot.Failure != nil:
		runtime.SetReadiness(lifecycle.ReadinessFailed)
	case snapshot.WorkerPendingSetup:
		runtime.SetReadiness(lifecycle.ReadinessReadyForSetup)
	case snapshot.WorkerReady:
		runtime.SetReadiness(lifecycle.ReadinessReady)
	default:
		runtime.SetReadiness(lifecycle.ReadinessStarting)
	}
	if snapshot.Registration.State != "" {
		runtime.SetRegistration(snapshot.Registration)
	}
	if snapshot.Connection.State != "" {
		runtime.SetConnection(snapshot.Connection)
	}
}
