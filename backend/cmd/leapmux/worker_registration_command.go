package main

import (
	"flag"
	"fmt"
	"time"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/worker/config"
)

type workerCredentialOptions struct {
	timeout     time.Duration
	explicitHub bool
}

func loadWorkerCredentialCommand(args []string, defaultTimeout time.Duration) (*config.Config, workerCredentialOptions, error) {
	var options workerCredentialOptions
	var flags *flag.FlagSet
	var timeout string
	cfg, showVersion, err := config.LoadWithOptions(args, config.LoadOptions{ConfigureFlags: func(fs *flag.FlagSet) {
		flags = fs
		fs.StringVar(&timeout, "timeout", defaultTimeout.String(), "maximum wait for the Hub. "+internalconfig.UnitSyntax)
	}})
	if err != nil {
		return nil, options, err
	}
	if showVersion {
		return nil, options, internalconfig.ErrVersionRequested
	}
	options.timeout, err = internalconfig.ParseDuration(timeout)
	if err != nil {
		return nil, options, fmt.Errorf("--timeout: %w", err)
	}
	if options.timeout <= 0 {
		return nil, options, fmt.Errorf("--timeout must be positive")
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "hub" {
			options.explicitHub = true
		}
	})
	return cfg, options, nil
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
