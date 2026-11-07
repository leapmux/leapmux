package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/solo"
)

type lifecycleConfiguration struct {
	role              lifecycle.Role
	dataDirectory     string
	configurationFile string
	runtimeArguments  []string
	explicitFlags     map[string]bool
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
	case "start", "restart":
		options.timeout = defaultStartTimeout
	case "stop":
		options.timeout = defaultStopTimeout
	}
	configuration, err := loadLifecycleConfiguration(mode, args, func(fs *flag.FlagSet) {
		switch verb {
		case "start", "restart", "stop", "status", "disable":
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

func loadLifecycleConfiguration(mode string, args []string, configure func(*flag.FlagSet), afterFlagsParsed func(*flag.FlagSet) error) (*lifecycleConfiguration, error) {
	role, err := lifecycle.ParseRole(mode)
	if err != nil {
		return nil, err
	}
	var flags *flag.FlagSet
	register := func(fs *flag.FlagSet) {
		flags = fs
		if configure != nil {
			configure(fs)
		}
	}
	configuration := &lifecycleConfiguration{role: role, checkStartup: func(*lifecycle.DirectoryLease) error { return nil }}
	configuration.readRegistration = func() (lifecycle.RegistrationStatus, error) {
		return lifecycle.RegistrationStatus{State: lifecycle.RegistrationNotApplicable}, nil
	}
	switch role {
	case lifecycle.RoleHub:
		cfg, version, loadErr := hubconfig.LoadWithOptions(args, hubconfig.LoadOptions{ConfigureFlags: register, AfterFlagsParsed: afterFlagsParsed})
		if loadErr != nil {
			return nil, loadErr
		}
		if version {
			return nil, internalconfig.ErrVersionRequested
		}
		configuration.dataDirectory = cfg.DataDir
	case lifecycle.RoleWorker:
		cfg, version, loadErr := workerconfig.LoadWithOptions(args, workerconfig.LoadOptions{ConfigureFlags: register, AfterFlagsParsed: afterFlagsParsed})
		if loadErr != nil {
			return nil, loadErr
		}
		if version {
			return nil, internalconfig.ErrVersionRequested
		}
		for _, name := range []string{"hub", "registration-key"} {
			present := false
			flags.Visit(func(value *flag.Flag) { present = present || value.Name == name })
			if present {
				return nil, fmt.Errorf("--%s belongs to `leapmux worker register`; execution uses the saved registration", name)
			}
		}
		configuration.dataDirectory = cfg.DataDir
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
			if _, stateErr := cfg.UsableRegistration(); stateErr != nil {
				return lifecycle.RegistrationStatus{State: lifecycle.RegistrationInvalid}, stateErr
			}
			return lifecycle.RegistrationStatus{
				State: lifecycle.RegistrationRegistered, WorkerID: new(state.WorkerID), HubURL: new(state.HubURL),
			}, nil
		}
	case lifecycle.RoleSolo, lifecycle.RoleDev:
		prepared, prepareErr := solo.Prepare(solo.Config{Args: args, DevMode: role == lifecycle.RoleDev, ConfigureFlags: register, AfterFlagsParsed: afterFlagsParsed})
		if prepareErr != nil {
			return nil, prepareErr
		}
		configuration.dataDirectory = prepared.DataDirectory()
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
