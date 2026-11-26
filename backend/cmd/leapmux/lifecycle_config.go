package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
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
	environmentFile string
	logFollow       bool
	logLines        int
}

// lifecycleFlags is what the registration of one lifecycle command binds: the
// options of the verb, the declaration of the mode's configuration flags, and
// the managed launch that `run --service-spec` selects. registerLifecycleFlags
// fills it, and load reads it after the parse, so the help, the parse and the
// load of a command read one declaration.
type lifecycleFlags struct {
	mode, verb string
	role       lifecycle.Role
	options    lifecycleOptions
	startup    string
	// Exactly one of hub, worker and solo declares the mode's flags.
	hub        *hubconfig.Flags
	worker     *workerconfig.Flags
	solo       *hubconfig.Flags
	soloConfig solo.Config
	// runtimeFlags lists the mode-owned runtime flag names that may be saved
	// into ServiceSpec.RuntimeArguments (excluding config and data-dir).
	runtimeFlags map[string]bool
	// managed is the selection of `run --service-spec`, which only the
	// service definition passes.
	managed         managedRunSelection
	managedSelected bool
}

// verbTimeout is the default --timeout of a lifecycle verb, and whether the
// verb takes one.
func verbTimeout(verb string) (time.Duration, bool) {
	switch verb {
	case "start":
		return defaultStartTimeout, true
	case "restart":
		return defaultRestartTimeout, true
	case "stop":
		return defaultStopTimeout, true
	case "status":
		return defaultStatusTimeout, true
	default:
		return 0, false
	}
}

// registerLifecycleFlags adds the flags of `leapmux <mode> <verb>` to fs: the
// mode's configuration flags that the verb accepts, and the verb's own.
func registerLifecycleFlags(mode, verb string, fs *pflag.FlagSet) (*lifecycleFlags, error) {
	role, err := lifecycle.ParseRole(mode)
	if err != nil {
		return nil, err
	}
	f := &lifecycleFlags{mode: mode, verb: verb, role: role, runtimeFlags: make(map[string]bool)}
	// nil keeps every mode flag of the loader.
	var modeFlags []string
	if selectsOnly(verb) {
		modeFlags = instanceSelectionFlags
	}
	existingFlags := make(map[string]bool)
	fs.VisitAll(func(fl *pflag.Flag) {
		existingFlags[fl.Name] = true
	})
	switch role {
	case lifecycle.RoleHub:
		f.hub = hubconfig.NewFlags(hubconfig.LoadOptions{CLIFlags: modeFlags})
		f.hub.Register(fs)
	case lifecycle.RoleWorker:
		f.worker = workerconfig.NewFlags(workerconfig.LoadOptions{CLIFlags: modeFlags})
		f.worker.Register(fs)
	case lifecycle.RoleSolo, lifecycle.RoleDev:
		f.soloConfig = solo.Config{DevMode: role == lifecycle.RoleDev, CLIFlags: modeFlags}
		// Solo's worker-scoped extras are flags too. A command that only
		// selects the instance registers none of them; only a starting
		// instance reads their values.
		if selectsOnly(verb) {
			f.soloConfig.ExtraFlags = []hubconfig.ExtraFlagDef{}
		}
		f.solo = solo.Flags(f.soloConfig)
		f.solo.Register(fs)
	}
	fs.VisitAll(func(fl *pflag.Flag) {
		if existingFlags[fl.Name] || fl.Name == "config" || fl.Name == "data-dir" {
			return
		}
		f.runtimeFlags[fl.Name] = true
	})
	if role == lifecycle.RoleWorker {
		// A person may try a registration flag on a runtime command. The
		// command accepts it only to refuse it with the command it belongs
		// to, so help omits it.
		for _, name := range workerRegistrationFlags {
			internalconfig.HiddenFunc(fs, name, "", func(string) error { return nil })
		}
	}
	if timeout, ok := verbTimeout(verb); ok {
		fs.Var(internalconfig.NewDurationFlag(&f.options.timeout, timeout), "timeout", "maximum wait for the operation; "+internalconfig.UnitSyntax)
	}
	if verb == "logs" {
		fs.BoolVarP(&f.options.logFollow, "follow", "f", false, "follow new log lines until interrupted")
		fs.IntVarP(&f.options.logLines, "lines", "n", defaultLogsLines, "number of trailing log lines to print (0 = all)")
	}
	switch verb {
	case "start", "restart", "enable":
		fs.StringVar(&f.startup, "startup", "", "automatic startup")
		cli.SetEnum(fs, "startup", string(lifecycle.StartupLogin), string(lifecycle.StartupBoot))
		fs.BoolVar(&f.options.yes, "yes", false, "apply the explained configuration without a consent prompt")
		fs.StringVar(&f.options.environmentFile, "environment-file", "", "absolute path to a YAML map of service environment variables")
	case "run":
		// The service definition passes this flag; a person never does, so
		// help omits it.
		internalconfig.HiddenFunc(fs, "service-spec", "private specification for native startup", func(path string) error {
			if f.managedSelected || path == "" {
				return fmt.Errorf("managed launch requires exactly one nonempty service specification")
			}
			f.managedSelected = true
			f.managed.specPath = path
			return nil
		})
	}
	return f, nil
}

// loadLifecycleCommand parses args as the command line of
// `leapmux <mode> <verb>` and loads it. A cobra leaf parses its own command
// line; this serves a command line that the program builds itself: the
// arguments a service specification saved, and the proposal of a start.
func loadLifecycleCommand(mode, verb string, args []string) (*lifecycleConfiguration, lifecycleOptions, error) {
	var flags *lifecycleFlags
	fs, err := internalconfig.ParseFlags("leapmux "+mode+" "+verb, args, func(fs *pflag.FlagSet) error {
		var regErr error
		flags, regErr = registerLifecycleFlags(mode, verb, fs)
		return regErr
	})
	if err != nil {
		return nil, lifecycleOptions{}, err
	}
	return flags.load(fs)
}

// load reads fs, which a parse filled after registerLifecycleFlags, and loads
// the configuration of the command. For `run --service-spec` it returns
// errManagedRunSelected before it loads anything: the saved specification
// supplies every setting.
func (f *lifecycleFlags) load(fs *pflag.FlagSet) (*lifecycleConfiguration, lifecycleOptions, error) {
	if _, ok := verbTimeout(f.verb); ok && f.options.timeout <= 0 {
		return nil, f.options, cli.Usagef("--timeout must be positive")
	}
	// An empty --startup= is a value the command line gave, so it fails as
	// any other unknown startup kind does; only an absent flag leaves the
	// choice to the saved specification or the prompt.
	if fs.Changed("startup") {
		kind, err := lifecycle.ParseStartupKind(f.startup)
		if err != nil {
			return nil, f.options, cli.Usagef("--startup %q is not a startup kind", f.startup).
				WithHint("give --startup login or --startup boot")
		}
		f.options.startup = kind
	}
	if f.worker != nil {
		if err := refuseWorkerRegistrationFlags(fs); err != nil {
			return nil, f.options, err
		}
	}
	if f.managedSelected {
		var invalid string
		fs.Visit(func(value *pflag.Flag) {
			switch value.Name {
			case "service-spec":
			case "data-dir":
				f.managed.dataDirectory = new(value.Value.String())
			default:
				invalid = value.Name
			}
		})
		if invalid != "" {
			return nil, f.options, cli.Usagef("managed launch cannot override --%s", invalid)
		}
		return nil, f.options, errManagedRunSelected
	}
	configuration, err := f.loadConfiguration(fs)
	return configuration, f.options, err
}

// workerRegistrationFlags are the flags of `leapmux daemon worker register` that a
// person may also try on a runtime command. The runtime commands accept them
// only to refuse them with the command they belong to; help omits them.
var workerRegistrationFlags = []string{"hub", "registration-key"}

// refuseWorkerRegistrationFlags refuses a registration flag on a runtime
// command. A registered Worker connects to the Hub URL that its registration
// saved, so a runtime --hub could only redirect the saved credentials, and a
// registration key has no use after registration.
func refuseWorkerRegistrationFlags(fs *pflag.FlagSet) error {
	var refused string
	fs.Visit(func(f *pflag.Flag) {
		if refused == "" && slices.Contains(workerRegistrationFlags, f.Name) {
			refused = f.Name
		}
	})
	if refused != "" {
		return cli.Usagef("--%s belongs to `leapmux daemon worker register`; this command uses the saved registration", refused)
	}
	return nil
}

// instanceSelectionFlags are the only mode flags of a command that acts on an
// instance without running it: stop, status, logs, disable, and the worker's
// credential commands. They select the data directory, as the config file and
// the environment also can. A runtime flag would be accepted and ignored.
var instanceSelectionFlags = []string{"data-dir"}

// selectsOnly reports whether a lifecycle command acts on an instance without
// running it or saving its runtime flags, so that it takes only
// instanceSelectionFlags from the mode.
func selectsOnly(verb string) bool {
	switch verb {
	case "stop", "status", "logs", "disable":
		return true
	default:
		return false
	}
}

// loadConfiguration loads the mode's configuration from fs.
func (f *lifecycleFlags) loadConfiguration(fs *pflag.FlagSet) (*lifecycleConfiguration, error) {
	role := f.role
	var err error
	configuration := &lifecycleConfiguration{role: role, checkStartup: func(*lifecycle.DirectoryLease) error { return nil }}
	configuration.validateRuntime = func() error { return nil }
	configuration.readRegistration = func() (lifecycle.RegistrationStatus, error) {
		return lifecycle.RegistrationStatus{State: lifecycle.RegistrationNotApplicable}, nil
	}
	switch role {
	case lifecycle.RoleHub:
		cfg, loadErr := f.hub.Load(fs)
		if loadErr != nil {
			return nil, loadErr
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
		cfg, loadErr := f.worker.Load(fs)
		if loadErr != nil {
			return nil, loadErr
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
		// Only a starting instance reads it: its Worker links this binary for
		// the processes that it spawns.
		f.soloConfig.ControlCLI = controlCLIExecutable()
		prepared, prepareErr := solo.PrepareParsed(f.soloConfig, f.solo, fs)
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
	configuration.configurationFile, err = filepath.Abs(internalconfig.ExpandHome(fs.Lookup("config").Value.String()))
	if err != nil {
		return nil, err
	}
	configuration.explicitFlags = make(map[string]bool)
	fs.Visit(func(value *pflag.Flag) {
		configuration.explicitFlags[value.Name] = true
		if !f.runtimeFlags[value.Name] {
			return
		}
		if list, ok := value.Value.(interface{ Get() []string }); ok {
			for _, item := range list.Get() {
				configuration.runtimeArguments = append(configuration.runtimeArguments, "--"+value.Name, item)
			}
			return
		}
		if value.Value.Type() == "bool" {
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
