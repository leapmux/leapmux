package lifecycle

import (
	"errors"
	"fmt"
	"path/filepath"
)

// StartServiceRuntime takes the data lease before revalidating the captured
// saved specification. A withdrawn or replaced specification cannot publish
// runtime resources or enter application startup.
func StartServiceRuntime(spec ServiceSpec) (*Runtime, error) {
	if _, err := ParseRole(spec.Mode.String()); err != nil {
		return nil, err
	}
	if _, err := ParseStartupKind(string(spec.Startup)); err != nil {
		return nil, err
	}
	canonical, err := CanonicalDir(spec.DataDirectory)
	if err != nil {
		return nil, err
	}
	account := CurrentAccountID()
	if spec.AccountID != account || spec.AccountName == "" || canonical != spec.DataDirectory ||
		spec.InstanceID != NewInstanceID(account, spec.Mode, canonical).String() {
		return nil, fmt.Errorf("lifecycle: the service specification does not match this account, mode, or canonical directory")
	}
	for _, path := range []struct{ label, value string }{
		{"executable", spec.Executable}, {"configuration", spec.ConfigFile}, {"working directory", spec.WorkingDirectory},
	} {
		if !filepath.IsAbs(path.value) {
			return nil, fmt.Errorf("lifecycle: the service %s path must be absolute", path.label)
		}
	}
	lease, err := AcquireDirectory(NewDirectoryOwner(spec.Mode, canonical), spec.Mode, canonical)
	if err != nil {
		return nil, err
	}
	current, err := ReadServiceSpec(pathsFor(canonical).ServiceSpec)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("lifecycle: revalidate the service specification: %w", err), lease.Close())
	}
	if !current.Equal(spec) {
		return nil, errors.Join(fmt.Errorf("lifecycle: the service specification changed before startup; retry the launch"), lease.Close())
	}
	return startRuntimeOwned(RuntimeConfig{Mode: spec.Mode, DataDir: canonical, Execution: ExecutionService}, lease)
}
