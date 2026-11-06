// Package servicelaunch holds the native-launch abstraction LeapMux's
// lifecycle commands share: one saved specification, generated into the
// platform's native startup mechanism (launchd, systemd, or the Windows
// service and task schedulers), previewed and applied with explicit
// consent, and inspected for drift.
//
// The package owns no mode-specific Hub or Worker behavior: a Manager
// installs, starts, stops, inspects, and diagnoses ONE definition the
// controller hands it, and every platform adapter lives in its own
// file.
package servicelaunch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// LaunchSpec is the native launch definition the controller derives
// from the saved service specification. Every path is absolute: a
// service must not depend on the directory the user ran `enable` from.
type LaunchSpec struct {
	InstanceID       string
	Mode             lifecycle.Role
	Startup          lifecycle.StartupKind
	AccountID        string
	AccountName      string
	Executable       string
	WorkingDirectory string
	ConfigFile       string
	DataDirectory    string
	RuntimeArguments []string
	// RuntimeEnvironment holds EXPLICIT overrides only, never the
	// calling shell's whole environment, and never a registration key.
	RuntimeEnvironment map[string]string
	// EnvironmentFile is an absolute path or nil.
	EnvironmentFile *string
}

// LaunchSpecFromService reads the saved specification into a launch
// definition.
func LaunchSpecFromService(spec lifecycle.ServiceSpec) (LaunchSpec, error) {
	if err := ValidateLaunchSpec(LaunchSpec{
		InstanceID:         spec.InstanceID,
		Mode:               spec.Mode,
		Startup:            spec.Startup,
		AccountID:          spec.AccountID,
		AccountName:        spec.AccountName,
		Executable:         spec.Executable,
		WorkingDirectory:   spec.WorkingDirectory,
		ConfigFile:         spec.ConfigFile,
		DataDirectory:      spec.DataDirectory,
		RuntimeArguments:   spec.RuntimeArguments,
		RuntimeEnvironment: spec.RuntimeEnvironment,
		EnvironmentFile:    spec.EnvironmentFile,
	}); err != nil {
		return LaunchSpec{}, err
	}
	return LaunchSpec{
		InstanceID:         spec.InstanceID,
		Mode:               spec.Mode,
		Startup:            spec.Startup,
		AccountID:          spec.AccountID,
		AccountName:        spec.AccountName,
		Executable:         spec.Executable,
		WorkingDirectory:   spec.WorkingDirectory,
		ConfigFile:         spec.ConfigFile,
		DataDirectory:      spec.DataDirectory,
		RuntimeArguments:   spec.RuntimeArguments,
		RuntimeEnvironment: spec.RuntimeEnvironment,
		EnvironmentFile:    spec.EnvironmentFile,
	}, nil
}

// ValidateLaunchSpec states what a native definition accepts: absolute
// paths, the four mode names, the two startup kinds, and no identity
// variable under an environment override.
func ValidateLaunchSpec(spec LaunchSpec) error {
	if spec.InstanceID == "" {
		return fmt.Errorf("servicelaunch: the specification needs its instance identity")
	}
	if _, err := lifecycle.ParseRole(string(spec.Mode)); err != nil {
		return err
	}
	if _, err := lifecycle.ParseStartupKind(string(spec.Startup)); err != nil {
		return err
	}
	if spec.AccountID == "" || spec.AccountName == "" {
		return fmt.Errorf("servicelaunch: the specification names the account it runs as")
	}
	for name, path := range map[string]string{
		"executable":        spec.Executable,
		"working directory": spec.WorkingDirectory,
		"config file":       spec.ConfigFile,
		"data directory":    spec.DataDirectory,
	} {
		if path == "" || !strings.HasPrefix(path, "/") && !windowsAbsolute(path) {
			return fmt.Errorf("servicelaunch: the %s must be an absolute path, got %q", name, path)
		}
	}
	if spec.EnvironmentFile != nil && *spec.EnvironmentFile == "" {
		return fmt.Errorf("servicelaunch: the environment file is an absolute path or absent")
	}
	for name := range spec.RuntimeEnvironment {
		if protectedIdentityVariables[name] {
			return fmt.Errorf("servicelaunch: %s states the service account's identity and cannot come from the runtime environment", name)
		}
	}
	return nil
}

// windowsAbsolute reports the Windows absolute-path spelling.
func windowsAbsolute(path string) bool {
	return len(path) >= 3 && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}

// protectedIdentityVariables are the environment names that state the
// service account's identity or home. An environment file or a runtime
// override cannot change them: the native definition's account is the
// one the operator chose, and a redirected HOME would move every
// default the service reads.
var protectedIdentityVariables = map[string]bool{
	"HOME":      true,
	"USER":      true,
	"LOGNAME":   true,
	"USERNAME":  true,
	"UID":       true,
	"EUID":      true,
	"SUDO_USER": true,
}

// ProtectedIdentityVariables lists the protected names, sorted, for the
// surfaces that explain a refusal.
func ProtectedIdentityVariables() []string {
	names := make([]string, 0, len(protectedIdentityVariables))
	for name := range protectedIdentityVariables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RunArguments composes the arguments the native definition runs: the
// mode's `run` command under its configuration file. A native
// definition NEVER invokes `start` -- a managed start that reached back
// into the controller could recurse.
func (s LaunchSpec) RunArguments() []string {
	args := []string{string(s.Mode), "run", "--config", s.ConfigFile}
	return append(args, s.RuntimeArguments...)
}

// Environment composes the runtime environment of the definition:
// explicit overrides, resolved. The platform adapter may add the
// environment file beside them.
func (s LaunchSpec) Environment() []string {
	names := make([]string, 0, len(s.RuntimeEnvironment))
	for name := range s.RuntimeEnvironment {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+s.RuntimeEnvironment[name])
	}
	return out
}

// SpecMatches states whether a saved specification and a launch
// definition describe the same launch, for drift detection.
func SpecMatches(spec lifecycle.ServiceSpec, launch LaunchSpec) bool {
	return spec.InstanceID == launch.InstanceID &&
		spec.Mode == launch.Mode &&
		spec.Startup == launch.Startup &&
		spec.AccountID == launch.AccountID &&
		spec.Executable == launch.Executable &&
		spec.ConfigFile == launch.ConfigFile &&
		spec.DataDirectory == launch.DataDirectory
}
