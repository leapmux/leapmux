package main

import (
	"flag"
	"fmt"
	"path/filepath"
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
	workerCredentialOptions
	hubURL          string
	registrationKey string
}

// workerCredentialOptions holds what every credential command takes besides
// the worker configuration.
type workerCredentialOptions struct {
	// configFile is the absolute path of the configuration file, which the
	// commands that a credential command prints next select.
	configFile string
	timeout    time.Duration
}

func loadWorkerRegisterCommand(args []string) (*config.Config, workerRegisterOptions, error) {
	var options workerRegisterOptions
	cfg, credentialOptions, err := loadWorkerCredentialCommand(args, "register", workerRegisterSummary, hub.DefaultRegistrationDeadline, func(fs *flag.FlagSet) {
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
	options.workerCredentialOptions = credentialOptions
	return cfg, options, nil
}

func loadWorkerDeregisterCommand(args []string) (*config.Config, workerCredentialOptions, error) {
	return loadWorkerCredentialCommand(args, "deregister", workerDeregisterSummary, defaultStopTimeout, nil)
}

// loadWorkerCredentialCommand loads the worker configuration that selects the
// data directory, plus --timeout and the flags that configure adds.
func loadWorkerCredentialCommand(args []string, command, summary string, defaultTimeout time.Duration, configure func(*flag.FlagSet)) (*config.Config, workerCredentialOptions, error) {
	options := workerCredentialOptions{timeout: defaultTimeout}
	cfg, showVersion, err := config.LoadWithOptions(args, config.LoadOptions{
		FlagSetName: "leapmux worker " + command,
		Description: summary + ".",
		// Registration sends no runtime setting, and deregistration reads
		// only the saved registration.
		CLIFlags: instanceSelectionFlags,
		ConfigureFlags: func(fs *flag.FlagSet) {
			fs.Var(internalconfig.NewDurationFlag(&options.timeout, defaultTimeout), "timeout", "maximum wait for the Hub. "+internalconfig.UnitSyntax)
			if configure != nil {
				configure(fs)
			}
		},
		AfterFlagsParsed: func(fs *flag.FlagSet) (err error) {
			options.configFile, err = filepath.Abs(internalconfig.ExpandHome(fs.Lookup("config").Value.String()))
			return err
		},
	})
	if err != nil {
		return nil, options, err
	}
	if showVersion {
		return nil, options, internalconfig.ErrVersionRequested
	}
	if options.timeout <= 0 {
		return nil, options, fmt.Errorf("--timeout must be positive")
	}
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
