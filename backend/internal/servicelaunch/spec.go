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
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/lifecycle"
)

// LaunchSpec derives a native definition from the saved service specification.
// The executable, working directory, configuration file, and data directory
// use absolute paths, independent of the directory the user ran enable from.
// LoadEnvironment validates the runtime environment selector and file.
type LaunchSpec lifecycle.ServiceSpec

// LaunchSpecFromService validates the saved specification as a native launch.
func LaunchSpecFromService(spec lifecycle.ServiceSpec) (LaunchSpec, error) {
	launch := LaunchSpec(spec)
	if err := ValidateLaunchSpec(launch); err != nil {
		return LaunchSpec{}, err
	}
	return launch, nil
}

// ValidateLaunchSpec checks native structure and explicit environment overrides.
// LoadEnvironment validates the runtime file when the operation needs it.
func ValidateLaunchSpec(spec LaunchSpec) error {
	if spec.InstanceID == "" {
		return fmt.Errorf("servicelaunch: the specification needs its instance identity")
	}
	for _, character := range spec.InstanceID {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'z' {
				return fmt.Errorf("servicelaunch: the instance identity must contain only lowercase letters and digits")
			}
		}
	}
	for index := 0; index < len(spec.RuntimeArguments); index++ {
		arg := spec.RuntimeArguments[index]
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("servicelaunch: a runtime argument contains a null character")
		}
		if !strings.HasPrefix(arg, "--") {
			return fmt.Errorf("servicelaunch: saved runtime arguments must use explicit flags")
		}
		key, _, inlineValue := strings.Cut(arg[2:], "=")
		switch key {
		case "config", "data-dir", "service-spec", "registration-key":
			return fmt.Errorf("servicelaunch: --%s cannot appear in saved runtime arguments", key)
		}
		if !inlineValue {
			index++
			if index == len(spec.RuntimeArguments) {
				return fmt.Errorf("servicelaunch: saved runtime flag --%s has no value", key)
			}
			if strings.ContainsRune(spec.RuntimeArguments[index], 0) {
				return fmt.Errorf("servicelaunch: a runtime argument contains a null character")
			}
		}
	}
	if _, err := lifecycle.ParseRole(string(spec.Mode)); err != nil {
		return err
	}
	if _, err := lifecycle.ParseStartupKind(string(spec.Startup)); err != nil {
		return err
	}
	if spec.AccountID == "" || spec.AccountName == "" {
		return fmt.Errorf("servicelaunch: the specification identifies the account it runs as")
	}
	for name, path := range map[string]string{
		"executable":        spec.Executable,
		"working directory": spec.WorkingDirectory,
		"config file":       spec.ConfigFile,
		"data directory":    spec.DataDirectory,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("servicelaunch: the %s must be an absolute path, got %q", name, path)
		}
	}
	_, err := normalizedRuntimeEnvironment(spec.RuntimeEnvironment, runtime.GOOS == "windows")
	return err
}

// protectedIdentityVariables are the environment names that state the
// service account's identity or home. An environment file or a runtime
// override cannot change them: the native definition's account is the
// one the operator chose, and a redirected HOME would move every
// default the service reads.
var protectedIdentityVariables = map[string]bool{
	"HOME":        true,
	"USER":        true,
	"LOGNAME":     true,
	"USERNAME":    true,
	"UID":         true,
	"EUID":        true,
	"SUDO_USER":   true,
	"USERPROFILE": true,
	"HOMEDRIVE":   true,
	"HOMEPATH":    true,
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
// mode's `run` command with its private specification. Runtime overrides
// stay in that specification, rather than a public native definition. A native
// definition NEVER invokes `start` -- a managed start that reached back
// into the controller could recurse.
func (s LaunchSpec) RunArguments() []string {
	return []string{string(s.Mode), "run", "--data-dir", s.DataDirectory, "--service-spec", lifecycle.PathsOf(s.DataDirectory).ServiceSpec}
}
