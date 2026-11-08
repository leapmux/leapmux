package main

import (
	"flag"
	"fmt"
	"time"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/hub"
)

// The one-line descriptions of the credential commands. The worker's command
// list and each command's own --help both print them.
const (
	workerRegisterSummary   = "Register with a Hub and save the credentials (never starts the worker)"
	workerDeregisterSummary = "Remove this worker's registration, keeping the worker database"
)

// workerRegisterOptions holds the flags of `leapmux worker register`.
//
// The Hub URL and the registration key are flags only, never configuration
// keys or environment variables. A URL from a file or the environment could
// register with a Hub that the operator never typed and use up the key there,
// and a key in worker.yaml would stay on disk next to every later command.
type workerRegisterOptions struct {
	hubURL          string
	registrationKey string
	timeout         time.Duration
}

func loadWorkerRegisterCommand(args []string) (*config.Config, workerRegisterOptions, error) {
	var options workerRegisterOptions
	cfg, timeout, err := loadWorkerCredentialCommand(args, "register", workerRegisterSummary, hub.DefaultRegistrationDeadline, func(fs *flag.FlagSet) {
		fs.StringVar(&options.hubURL, "hub", "", "URL of the Hub to register with: http[s]://..., unix:<socket-path>, or npipe:<pipe-name> (required)")
		fs.StringVar(&options.registrationKey, "registration-key", "", "registration key from the Register worker dialog of the Hub (required)")
	})
	if err != nil {
		return nil, options, err
	}
	if options.hubURL == "" {
		return nil, options, fmt.Errorf("--hub is required: the URL of the Hub this worker registers with")
	}
	if options.registrationKey == "" {
		return nil, options, fmt.Errorf("--registration-key is required: generate one in the Register worker dialog of the Hub")
	}
	options.timeout = timeout
	return cfg, options, nil
}

func loadWorkerDeregisterCommand(args []string) (*config.Config, time.Duration, error) {
	return loadWorkerCredentialCommand(args, "deregister", workerDeregisterSummary, defaultStopTimeout, nil)
}

// loadWorkerCredentialCommand loads the worker configuration that selects the
// data directory, plus --timeout and the flags that configure adds.
func loadWorkerCredentialCommand(args []string, command, summary string, defaultTimeout time.Duration, configure func(*flag.FlagSet)) (*config.Config, time.Duration, error) {
	timeout := defaultTimeout
	cfg, showVersion, err := config.LoadWithOptions(args, config.LoadOptions{
		FlagSetName: "leapmux worker " + command,
		Description: summary + ".",
		// Registration sends no runtime setting, and deregistration reads
		// only the saved registration.
		CLIFlags: instanceSelectionFlags,
		ConfigureFlags: func(fs *flag.FlagSet) {
			fs.Var(internalconfig.NewDurationFlag(&timeout, defaultTimeout), "timeout", "maximum wait for the Hub. "+internalconfig.UnitSyntax)
			if configure != nil {
				configure(fs)
			}
		},
	})
	if err != nil {
		return nil, 0, err
	}
	if showVersion {
		return nil, 0, internalconfig.ErrVersionRequested
	}
	if timeout <= 0 {
		return nil, 0, fmt.Errorf("--timeout must be positive")
	}
	return cfg, timeout, nil
}

// workerRegistrationFiles is the persistence boundary of one credential command.
// The supplied Config remains the sole authority for every path.
type workerRegistrationFiles interface {
	LoadState() (*config.State, error)
	SaveState(*config.State) error
	ClearState() error
	LoadPendingRegistration() (*config.PendingRegistration, error)
	SavePendingRegistration(*config.PendingRegistration) error
	RemovePendingRegistration() error
}

func configuredWorkerRegistrationFiles(cfg *config.Config) workerRegistrationFiles { return cfg }
