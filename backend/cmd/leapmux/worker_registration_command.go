package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
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

// defaultDeregisterTimeout limits the wait for the Hub. Deregistration
// requires a stopped worker, so it waits for no shutdown.
const defaultDeregisterTimeout = 30 * time.Second

// workerRegisterOptions holds the flags of `leapmux daemon worker register`.
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

// newWorkerRegisterCommand builds `leapmux daemon worker register`.
func newWorkerRegisterCommand(filesFor func(*config.Config) workerRegistrationFiles) *cobra.Command {
	var flags *workerRegisterFlags
	return cli.NewLeaf(cli.Leaf{
		Use:   "register",
		Short: workerRegisterSummary,
		Long: workerRegisterSummary + `.

The command saves an attempt record before it sends the first request. When
the command fails or stops after the Hub accepted the key, run the same command
again: it finishes that registration instead of using a second key.`,
		Mutates:          true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Comment: "Register with the key from the Register worker dialog of the Hub.", Command: "leapmux daemon worker register --hub https://hub.example.com --registration-key KEY"},
			{Comment: "Register an instance in a data directory of its own.", Command: "leapmux daemon worker register --hub unix:/run/leapmux/hub.sock --registration-key KEY --data-dir /srv/leapmux-worker"},
		},
		Flags: func(fs *pflag.FlagSet) { flags = registerWorkerRegisterFlags(fs) },
		Run: func(inv *cli.Invocation) (any, error) {
			cfg, options, err := flags.load(inv.Command.Flags())
			if err != nil {
				return nil, err
			}
			res, err := registerWorker(inv.Context, inv.Printer, cfg, options, filesFor(cfg))
			if err != nil {
				return nil, err
			}
			if inv.Printer != nil && inv.Printer.Format != cli.FormatText {
				return res, nil
			}
			return nil, nil
		},
	})
}

// newWorkerDeregisterCommand builds `leapmux daemon worker deregister`.
func newWorkerDeregisterCommand(filesFor func(*config.Config) workerRegistrationFiles) *cobra.Command {
	var flags *workerCredentialFlags
	return cli.NewLeaf(cli.Leaf{
		Use:   "deregister",
		Short: workerDeregisterSummary,
		Long: workerDeregisterSummary + `.

The Worker must be stopped. The Hub deletes the Worker first, and only then
does the command remove the saved credentials, so a failed request leaves the
registration usable.`,
		Mutates:          true,
		Destructive:      true,
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux daemon worker deregister"},
			{Comment: "Deregister an instance in a data directory of its own.", Command: "leapmux daemon worker deregister --data-dir /srv/leapmux-worker"},
		},
		Flags: func(fs *pflag.FlagSet) { flags = registerWorkerCredentialFlags(fs, defaultDeregisterTimeout) },
		Run: func(inv *cli.Invocation) (any, error) {
			cfg, options, err := flags.load(inv.Command.Flags())
			if err != nil {
				return nil, err
			}
			res, err := deregisterWorker(inv.Context, inv.Printer, cfg, options, filesFor(cfg))
			if err != nil {
				return nil, err
			}
			if inv.Printer != nil && inv.Printer.Format != cli.FormatText {
				return res, nil
			}
			return nil, nil
		},
	})
}

// workerRegisterFlags is what the registration of `leapmux daemon worker register`
// binds: the flags every credential command takes, plus the Hub URL and the
// registration key.
type workerRegisterFlags struct {
	credential      *workerCredentialFlags
	hubURL          string
	registrationKey string
}

func registerWorkerRegisterFlags(fs *pflag.FlagSet) *workerRegisterFlags {
	f := &workerRegisterFlags{}
	fs.StringVar(&f.hubURL, "hub", "", "URL of the Hub to register with: http[s]://..., unix:<socket-path>, or npipe:<pipe-name>")
	cli.MarkRequired(fs, "hub")
	fs.StringVar(&f.registrationKey, "registration-key", "", "registration key from the Register worker dialog of the Hub")
	cli.MarkRequired(fs, "registration-key")
	f.credential = registerWorkerCredentialFlags(fs, hub.DefaultRegistrationDeadline)
	return f
}

// load reads fs, which a parse filled after registerWorkerRegisterFlags. A
// command line that leaves out --hub or --registration-key fails before
// this, so load refuses only an empty value.
func (f *workerRegisterFlags) load(fs *pflag.FlagSet) (*config.Config, workerRegisterOptions, error) {
	options := workerRegisterOptions{hubURL: f.hubURL, registrationKey: f.registrationKey}
	if options.hubURL == "" {
		return nil, options, cli.Usagef("--hub is empty").WithHint("give the URL of the Hub that this Worker registers with")
	}
	if options.registrationKey == "" {
		return nil, options, cli.Usagef("--registration-key is empty").WithHint("generate a key in the Register worker dialog of the Hub")
	}
	cfg, credentialOptions, err := f.credential.load(fs)
	if err != nil {
		return nil, options, err
	}
	options.workerCredentialOptions = credentialOptions
	return cfg, options, nil
}

// workerCredentialFlags is what the registration of one credential command
// binds: the worker flags that select the data directory, and --timeout.
type workerCredentialFlags struct {
	worker  *config.Flags
	timeout time.Duration
}

func registerWorkerCredentialFlags(fs *pflag.FlagSet, defaultTimeout time.Duration) *workerCredentialFlags {
	// Registration sends no runtime setting, and deregistration reads only
	// the saved registration, so neither takes a runtime flag.
	f := &workerCredentialFlags{worker: config.NewFlags(config.LoadOptions{CLIFlags: instanceSelectionFlags})}
	fs.Var(internalconfig.NewDurationFlag(&f.timeout, defaultTimeout), "timeout", "maximum wait for the Hub; "+internalconfig.UnitSyntax)
	f.worker.Register(fs)
	return f
}

// load reads fs, which a parse filled after registerWorkerCredentialFlags,
// and loads the worker configuration.
func (f *workerCredentialFlags) load(fs *pflag.FlagSet) (*config.Config, workerCredentialOptions, error) {
	options := workerCredentialOptions{timeout: f.timeout}
	if options.timeout <= 0 {
		return nil, options, cli.Usagef("--timeout must be positive")
	}
	cfg, err := f.worker.Load(fs)
	if err != nil {
		return nil, options, err
	}
	options.configFile, err = filepath.Abs(internalconfig.ExpandHome(fs.Lookup("config").Value.String()))
	if err != nil {
		return nil, options, fmt.Errorf("resolve the configuration file: %w", err)
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
