package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/solo"
)

type lifecycleConfiguration struct {
	executeForeground foregroundExecution
	role              lifecycle.Role
	dataDirectory     string
	configurationFile string
	runtimeArguments  []string
	explicitFlags     map[string]bool
	validateRuntime   func() error
	checkStartup      func(*lifecycle.DirectoryLease) error
	readRegistration  func() (lifecycle.RegistrationStatus, error)
}

type lifecycleOptions struct {
	timeout         time.Duration
	startup         lifecycle.StartupKind
	yes             bool
	json            bool
	environmentFile string
}

func loadLifecycleCommand(mode, verb string, args []string) (*lifecycleConfiguration, lifecycleOptions, error) {
	options := lifecycleOptions{timeout: defaultStatusTimeout}
	switch verb {
	case "start":
		options.timeout = defaultStartTimeout
	case "restart":
		options.timeout = defaultRestartTimeout
	case "stop":
		options.timeout = defaultStopTimeout
	}
	configuration, err := loadLifecycleConfiguration(mode, verb, args, func(fs *flag.FlagSet) {
		switch verb {
		case "start", "restart", "stop", "status":
			fs.Var(internalconfig.NewDurationFlag(&options.timeout, options.timeout), "timeout", "maximum wait for the operation")
		}
		if verb == "status" {
			fs.BoolVar(&options.json, "json", false, "print status as JSON")
		}
		switch verb {
		case "start", "restart", "enable":
			fs.Func("startup", "automatic startup: login or boot", func(value string) error {
				kind, parseErr := lifecycle.ParseStartupKind(value)
				options.startup = kind
				return parseErr
			})
			fs.BoolVar(&options.yes, "yes", false, "apply the explained configuration without a consent prompt")
			fs.StringVar(&options.environmentFile, "environment-file", "", "absolute path to a YAML map of service environment variables")
		}
	}, nil)
	if err != nil {
		return nil, options, err
	}
	if options.timeout <= 0 {
		return nil, options, fmt.Errorf("--timeout must be positive")
	}
	return configuration, options, nil
}

// workerRegistrationFlags are the flags of `leapmux worker register` that a
// person may also try on a runtime command. The runtime commands accept them
// only to refuse them with the command they belong to; help omits them.
var workerRegistrationFlags = []string{"hub", "registration-key"}

// refuseWorkerRegistrationFlags refuses a registration flag on a runtime
// command. A registered Worker connects to the Hub URL that its registration
// saved, so a runtime --hub could only redirect the saved credentials, and a
// registration key has no use after registration.
func refuseWorkerRegistrationFlags(fs *flag.FlagSet) error {
	var refused string
	fs.Visit(func(f *flag.Flag) {
		if refused == "" && slices.Contains(workerRegistrationFlags, f.Name) {
			refused = f.Name
		}
	})
	if refused != "" {
		return fmt.Errorf("--%s belongs to `leapmux worker register`; this command uses the saved registration", refused)
	}
	return nil
}

// instanceSelectionFlags are the only mode flags of a command that acts on an
// instance without running it: stop, status, disable, and the worker's
// credential commands. They select the data directory, as the config file and
// the environment also can. A runtime flag would be accepted and ignored.
var instanceSelectionFlags = []string{"data-dir"}

// selectsOnly reports whether a lifecycle command acts on an instance without
// running it or saving its runtime flags, so that it takes only
// instanceSelectionFlags from the mode.
func selectsOnly(verb string) bool {
	switch verb {
	case "stop", "status", "disable":
		return true
	default:
		return false
	}
}

// loadLifecycleConfiguration loads the configuration of one lifecycle command.
// verb selects the help header (`leapmux <mode> <verb>` and the command's
// summary) and the mode flags that the command accepts.
func loadLifecycleConfiguration(mode, verb string, args []string, configure func(*flag.FlagSet), afterFlagsParsed func(*flag.FlagSet) error) (*lifecycleConfiguration, error) {
	role, err := lifecycle.ParseRole(mode)
	if err != nil {
		return nil, err
	}
	flagSetName := "leapmux " + mode + " " + verb
	description := lifecycleSummary(mode, verb) + "."
	// nil keeps every mode flag of the loader.
	var modeFlags []string
	if selectsOnly(verb) {
		modeFlags = instanceSelectionFlags
	}
	var flags *flag.FlagSet
	register := func(fs *flag.FlagSet) {
		flags = fs
		if configure != nil {
			configure(fs)
		}
	}
	configuration := &lifecycleConfiguration{role: role, checkStartup: func(*lifecycle.DirectoryLease) error { return nil }}
	configuration.validateRuntime = func() error { return nil }
	configuration.readRegistration = func() (lifecycle.RegistrationStatus, error) {
		return lifecycle.RegistrationStatus{State: lifecycle.RegistrationNotApplicable}, nil
	}
	switch role {
	case lifecycle.RoleHub:
		cfg, version, loadErr := hubconfig.LoadWithOptions(args, hubconfig.LoadOptions{
			FlagSetName: flagSetName, Description: description, CLIFlags: modeFlags,
			ConfigureFlags: register, AfterFlagsParsed: afterFlagsParsed,
		})
		if loadErr != nil {
			return nil, loadErr
		}
		if version {
			return nil, internalconfig.ErrVersionRequested
		}
		cfg.DataDir, loadErr = lifecycle.CanonicalDir(cfg.DataDir)
		if loadErr != nil {
			return nil, loadErr
		}
		configuration.dataDirectory = cfg.DataDir
		configuration.executeForeground = func(ctx context.Context, runtime *lifecycle.Runtime) error {
			return runHubForeground(ctx, runtime, cfg)
		}
	case lifecycle.RoleWorker:
		cfg, version, loadErr := workerconfig.LoadWithOptions(args, workerconfig.LoadOptions{
			FlagSetName: flagSetName, Description: description, CLIFlags: modeFlags,
			ConfigureFlags: func(fs *flag.FlagSet) {
				register(fs)
				for _, name := range workerRegistrationFlags {
					internalconfig.HiddenFunc(fs, name, "", func(string) error { return nil })
				}
			},
			AfterFlagsParsed: func(fs *flag.FlagSet) error {
				if err := refuseWorkerRegistrationFlags(fs); err != nil {
					return err
				}
				if afterFlagsParsed != nil {
					return afterFlagsParsed(fs)
				}
				return nil
			},
		})
		if loadErr != nil {
			return nil, loadErr
		}
		if version {
			return nil, internalconfig.ErrVersionRequested
		}
		cfg.DataDir, loadErr = lifecycle.CanonicalDir(cfg.DataDir)
		if loadErr != nil {
			return nil, loadErr
		}
		configuration.dataDirectory = cfg.DataDir
		configuration.executeForeground = func(ctx context.Context, runtime *lifecycle.Runtime) error {
			return runWorkerForeground(ctx, runtime, cfg)
		}
		configuration.validateRuntime = cfg.ValidateRuntime
		configuration.checkStartup = func(directory *lifecycle.DirectoryLease) error {
			if directory == nil || directory.Closed() || directory.Mode() != lifecycle.RoleWorker || directory.Path() != configuration.dataDirectory {
				return fmt.Errorf("worker startup requires its held data directory lease")
			}
			_, registrationErr := cfg.UsableRegistration()
			return registrationErr
		}
		configuration.readRegistration = func() (lifecycle.RegistrationStatus, error) {
			state, stateErr := cfg.LoadState()
			if stateErr != nil {
				return lifecycle.RegistrationStatus{State: lifecycle.RegistrationInvalid}, stateErr
			}
			if state == nil {
				pending, pendingErr := cfg.LoadPendingRegistration()
				if pendingErr != nil {
					return lifecycle.RegistrationStatus{State: lifecycle.RegistrationInvalid}, pendingErr
				}
				if pending != nil {
					return lifecycle.RegistrationStatus{State: lifecycle.RegistrationPending, HubURL: new(pending.HubURL)}, nil
				}
				return lifecycle.RegistrationStatus{State: lifecycle.RegistrationUnregistered}, nil
			}
			if _, stateErr := state.UsableKeypair(); stateErr != nil {
				return lifecycle.RegistrationStatus{State: lifecycle.RegistrationInvalid}, stateErr
			}
			return lifecycle.RegistrationStatus{
				State: lifecycle.RegistrationRegistered, WorkerID: new(state.WorkerID), HubURL: new(state.HubURL),
			}, nil
		}
	case lifecycle.RoleSolo, lifecycle.RoleDev:
		// Solo's worker-scoped extras are flags too. A command that only
		// selects the instance registers none of them; only a starting
		// instance reads their values.
		var extraFlags []hubconfig.ExtraFlagDef
		if selectsOnly(verb) {
			extraFlags = []hubconfig.ExtraFlagDef{}
		}
		prepared, prepareErr := solo.Prepare(solo.Config{
			Args: args, DevMode: role == lifecycle.RoleDev,
			FlagSetName: flagSetName, Description: description,
			CLIFlags: modeFlags, ExtraFlags: extraFlags,
			ConfigureFlags: register, AfterFlagsParsed: afterFlagsParsed,
		})
		if prepareErr != nil {
			return nil, prepareErr
		}
		configuration.dataDirectory = prepared.DataDirectory()
		configuration.executeForeground = func(ctx context.Context, runtime *lifecycle.Runtime) error {
			return runSoloForeground(ctx, runtime, prepared)
		}
	}
	configuration.dataDirectory, err = lifecycle.CanonicalDir(configuration.dataDirectory)
	if err != nil {
		return nil, err
	}
	configuration.configurationFile, err = filepath.Abs(internalconfig.ExpandHome(flags.Lookup("config").Value.String()))
	if err != nil {
		return nil, err
	}
	configuration.explicitFlags = make(map[string]bool)
	flags.Visit(func(value *flag.Flag) {
		configuration.explicitFlags[value.Name] = true
		switch value.Name {
		case "config", "data-dir", "version", "timeout", "startup", "yes", "json", "environment-file", "service-spec":
			return
		}
		if list, ok := value.Value.(interface{ Get() []string }); ok {
			for _, item := range list.Get() {
				configuration.runtimeArguments = append(configuration.runtimeArguments, "--"+value.Name, item)
			}
			return
		}
		if boolean, ok := value.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			configuration.runtimeArguments = append(configuration.runtimeArguments, "--"+value.Name+"="+value.Value.String())
			return
		}
		configuration.runtimeArguments = append(configuration.runtimeArguments, "--"+value.Name, value.Value.String())
	})
	if configuration.runtimeArguments == nil {
		configuration.runtimeArguments = []string{}
	}
	return configuration, nil
}
