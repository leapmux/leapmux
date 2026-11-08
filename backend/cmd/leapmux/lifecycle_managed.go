package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/servicelaunch"
	"github.com/leapmux/leapmux/lifecycle"
)

var errManagedRunSelected = errors.New("managed run selected after flag parsing")

type managedRunSelection struct {
	specPath      string
	dataDirectory *string
}

// managedRun validates parsed selectors against the private saved specification
// before runtime publication. The specification supplies all effective settings.
func managedRun(mode string, selection managedRunSelection) (*lifecycle.ServiceSpec, []string, error) {
	path := selection.specPath
	if !filepath.IsAbs(path) {
		return nil, nil, fmt.Errorf("managed launch requires an absolute service specification path")
	}
	spec, err := lifecycle.ReadServiceSpec(path)
	if err != nil {
		return nil, nil, fmt.Errorf("managed launch cannot read its service specification: %w", err)
	}
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("managed launch has an invalid specification: %w", err)
	}
	if _, err := launch.LoadEnvironment(); err != nil {
		return nil, nil, fmt.Errorf("managed launch has an invalid specification: %w", err)
	}
	canonical, err := lifecycle.CanonicalDir(spec.DataDirectory)
	if err != nil {
		return nil, nil, fmt.Errorf("managed launch cannot resolve its data directory: %w", err)
	}
	if spec.Mode.String() != mode || spec.AccountID != lifecycle.CurrentAccountID() || spec.AccountName != currentAccountName() ||
		canonical != spec.DataDirectory || filepath.Clean(path) != lifecycle.PathsOf(canonical).ServiceSpec ||
		spec.InstanceID != lifecycle.NewInstanceID(spec.AccountID, spec.Mode, canonical).String() {
		return nil, nil, fmt.Errorf("managed launch specification does not match the selected account, mode, or data directory")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("managed launch cannot identify its executable: %w", err)
	}
	actual, actualErr := os.Stat(executable)
	expected, expectedErr := os.Stat(spec.Executable)
	if actualErr != nil || expectedErr != nil || !os.SameFile(actual, expected) {
		return nil, nil, fmt.Errorf("managed launch executable differs from the saved specification")
	}
	effective := []string{"--config", spec.ConfigFile, "--data-dir", spec.DataDirectory}
	effective = append(effective, spec.RuntimeArguments...)
	if selection.dataDirectory != nil && *selection.dataDirectory != spec.DataDirectory {
		return nil, nil, fmt.Errorf("managed launch data directory differs from the saved specification")
	}
	return &spec, effective, nil
}

func applyManagedEnvironment(spec lifecycle.ServiceSpec) (func() error, error) {
	launch, err := servicelaunch.LaunchSpecFromService(spec)
	if err != nil {
		return nil, err
	}
	values, err := launch.LoadEnvironment()
	if err != nil {
		return nil, err
	}
	account, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("managed launch cannot read its account: %w", err)
	}
	// The account database supplies identity values, independent of the
	// invoking shell and any administrator who authorized installation.
	for key, value := range map[string]string{
		"HOME": account.HomeDir, "USER": account.Username, "LOGNAME": account.Username,
		"USERNAME": account.Username, "UID": account.Uid, "EUID": account.Uid,
		"USERPROFILE": account.HomeDir, "HOMEDRIVE": filepath.VolumeName(account.HomeDir),
		"HOMEPATH": strings.TrimPrefix(account.HomeDir, filepath.VolumeName(account.HomeDir)),
	} {
		values[key] = value
	}
	prior := make(map[string]*string, len(values))
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	restore := func() error {
		var result error
		for key, value := range prior {
			if value == nil {
				result = errors.Join(result, os.Unsetenv(key))
			} else {
				result = errors.Join(result, os.Setenv(key, *value))
			}
		}
		return errors.Join(result, os.Chdir(directory))
	}
	for key, value := range values {
		if original, exists := os.LookupEnv(key); exists {
			prior[key] = new(original)
		} else {
			prior[key] = nil
		}
		if err := os.Setenv(key, value); err != nil {
			return nil, errors.Join(err, restore())
		}
	}
	if err := os.Chdir(spec.WorkingDirectory); err != nil {
		return nil, errors.Join(err, restore())
	}
	return restore, nil
}
