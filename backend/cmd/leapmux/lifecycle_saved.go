package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalconfig "github.com/leapmux/leapmux/internal/config"
	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
)

func commandServiceSpec(configuration *lifecycleConfiguration, options lifecycleOptions, startup lifecycle.StartupKind, saved *lifecycle.ServiceSpec, refreshExecutable bool) (lifecycle.ServiceSpec, error) {
	if saved == nil {
		return serviceSpecFromConfiguration(configuration, options, startup)
	}
	spec := *saved
	if spec.Mode != configuration.role || spec.DataDirectory != configuration.dataDirectory || spec.AccountID != lifecycle.CurrentAccountID() ||
		spec.AccountName != currentAccountName() || spec.InstanceID != lifecycle.NewInstanceID(spec.AccountID, spec.Mode, spec.DataDirectory).String() {
		return lifecycle.ServiceSpec{}, fmt.Errorf("the saved service specification belongs to another account, mode, or data directory")
	}
	spec.Startup = startup
	if refreshExecutable {
		current, err := os.Executable()
		if err != nil {
			return lifecycle.ServiceSpec{}, err
		}
		spec.Executable = current
	}
	if configuration.explicitFlags["config"] {
		spec.ConfigFile = configuration.configurationFile
	}
	var err error
	if len(configuration.runtimeArguments) > 0 {
		spec.RuntimeArguments, err = mergeRuntimeArguments(spec.RuntimeArguments, configuration.runtimeArguments)
		if err != nil {
			return lifecycle.ServiceSpec{}, err
		}
	}
	if configuration.explicitFlags["environment-file"] {
		spec.EnvironmentFile = nil
		if options.environmentFile != "" {
			path, err := filepath.Abs(internalconfig.ExpandHome(options.environmentFile))
			if err != nil {
				return lifecycle.ServiceSpec{}, err
			}
			spec.EnvironmentFile = &path
		}
		if err := selectServiceEnvironmentFile(&spec); err != nil {
			return lifecycle.ServiceSpec{}, err
		}
	}
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	if _, err := launch.LoadEnvironment(); err != nil {
		return lifecycle.ServiceSpec{}, err
	}
	return spec, nil
}

// A file's PATH replaces the automatically captured shell PATH. Other saved
// explicit overrides keep their documented precedence over file values.
func selectServiceEnvironmentFile(spec *lifecycle.ServiceSpec) error {
	fileValues, err := (servicelaunch.LaunchSpec{EnvironmentFile: spec.EnvironmentFile}).LoadEnvironment()
	if err != nil {
		return err
	}
	values := maps.Clone(spec.RuntimeEnvironment)
	if values == nil {
		values = make(map[string]string)
	}
	if _, provided := fileValues["PATH"]; provided {
		for key := range values {
			if strings.EqualFold(key, "PATH") {
				delete(values, key)
			}
		}
	} else if _, captured := values["PATH"]; !captured {
		values["PATH"] = os.Getenv("PATH")
	}
	spec.RuntimeEnvironment = values
	return nil
}

// Runtime arguments use the canonical form emitted by the shared flag loader:
// a boolean has =value, and every other flag precedes exactly one value.
// An explicit repeatable flag replaces that flag's saved values as a group.
func mergeRuntimeArguments(saved, overrides []string) ([]string, error) {
	groups := make(map[string][]string)
	for _, source := range [][]string{saved, overrides} {
		parsed := make(map[string][]string)
		for index := 0; index < len(source); index++ {
			argument := source[index]
			if !strings.HasPrefix(argument, "--") {
				return nil, fmt.Errorf("the saved runtime arguments contain an invalid flag")
			}
			key, _, inlineValue := strings.Cut(argument[2:], "=")
			parsed[key] = append(parsed[key], argument)
			if !inlineValue {
				index++
				if index == len(source) {
					return nil, fmt.Errorf("the saved runtime flag --%s has no value", key)
				}
				parsed[key] = append(parsed[key], source[index])
			}
		}
		for key, values := range parsed {
			groups[key] = values
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []string
	for _, key := range keys {
		result = append(result, groups[key]...)
	}
	return result, nil
}
