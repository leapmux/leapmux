package procutil

import (
	"errors"
	"fmt"
	"math"
)

// heldJobProcess identifies and assigns the same open process handle.
type heldJobProcess interface {
	Identity() (ProcessIdentity, bool, error)
	Assign(uintptr) error
	Close() error
}

// processJobDriver supplies platform operations without global test overrides.
type processJobDriver interface {
	CreateJob() (uintptr, error)
	ConfigureJob(uintptr) error
	OpenProcess(int) (heldJobProcess, error)
	TerminateJob(uintptr) error
	CloseJob(uintptr) error
}

// assignOwnedJob assigns the opened process handle to its native job.
func assignOwnedJob(root ProcessIdentity, driver processJobDriver) (assigned uintptr, result error) {
	if root.PID <= 0 || root.PID > math.MaxInt32 || root.StartTime <= 0 {
		return 0, errors.New("the owned process requires a valid creation identity")
	}
	job, err := driver.CreateJob()
	if err != nil {
		return 0, fmt.Errorf("create the owned process job: %w", err)
	}
	closeUninstalledJob := func(operationErr error) (uintptr, error) {
		if closeErr := driver.CloseJob(job); closeErr != nil {
			operationErr = errors.Join(operationErr, fmt.Errorf("close the owned process job: %w", closeErr))
		}
		return 0, operationErr
	}
	if err := driver.ConfigureJob(job); err != nil {
		return closeUninstalledJob(fmt.Errorf("configure the owned process job: %w", err))
	}
	process, err := driver.OpenProcess(root.PID)
	if err != nil {
		return closeUninstalledJob(fmt.Errorf("open the owned process: %w", err))
	}
	defer func() {
		if closeErr := process.Close(); closeErr != nil {
			result = errors.Join(result, fmt.Errorf("close the owned process handle: %w", closeErr))
			if assigned != 0 {
				assigned, result = closeUninstalledJob(result)
			}
		}
	}()
	identity, runs, err := process.Identity()
	if err != nil {
		return closeUninstalledJob(fmt.Errorf("read the held process identity: %w", err))
	}
	if identity != root || !runs {
		return closeUninstalledJob(errors.New("the held process no longer matches the live creation identity"))
	}
	if err := process.Assign(job); err != nil {
		return closeUninstalledJob(fmt.Errorf("assign the owned process job: %w", err))
	}
	return job, nil
}

// terminateOwnedJob ends the assigned processes and closes the job handle.
func terminateOwnedJob(job uintptr, driver processJobDriver) error {
	terminateErr := driver.TerminateJob(job)
	closeErr := driver.CloseJob(job)
	return errors.Join(terminateErr, closeErr)
}
