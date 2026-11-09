package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// ValidateDirectorySelection refuses another account, mode, or containing
// instance before a command reads or changes that instance's resources.
func ValidateDirectorySelection(role Role, canonical string) error {
	if _, err := ParseRole(role.String()); err != nil {
		return err
	}
	owner, err := VerifyOwnership(canonical)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	expected := NewDirectoryOwner(role, canonical)
	if owner.movedTo(expected, role, canonical) {
		// The next command that takes the data lock adopts the directory.
		return nil
	}
	if owner.AccountID != expected.AccountID || owner.Mode != role || owner.DataDirectory != canonical || owner.OwnerMode != role || owner.OwnerDataDir != canonical {
		return &OwnershipConflict{OwningMode: owner.OwnerMode, OwningRoot: owner.OwnerDataDir, Component: owner.Mode, Path: canonical}
	}
	return nil
}

// ShutdownTimeout is how long an instance may take to finish its shutdown
// after a stop request or SIGTERM. launchd and systemd send SIGKILL to an
// instance that outlives it after SIGTERM, and `stop` waits that long by
// default.
const ShutdownTimeout = 3 * time.Minute

// ShutdownDrainTimeout is how long after its start a shutdown may wait for
// work to finish. Every wait of the shutdown ends by then, at its own limit or
// at this one, so every step that persists state starts before it: a database
// write, a database close, the runtime record, a lock release. The time from
// here to ShutdownTimeout, at least one minute, is the reserve for those
// steps, because a SIGKILL ends the steps that it interrupts.
//
// Each mode holds one drain.Deadline with this budget for its shutdown, and a
// combined instance shares one between its Hub and its Worker.
const ShutdownDrainTimeout = 2 * time.Minute

// Stop requests authenticated shutdown and waits for the runtime lock to be
// released after the complete application join. The result reports whether
// the command found an active runtime. It never terminates a process by PID.
func Stop(ctx context.Context, role Role, dataDirectory string) (bool, error) {
	return stopWithRequester(ctx, role, dataDirectory, RequestStop)
}

func stopWithRequester(ctx context.Context, role Role, dataDirectory string, request func(context.Context, RuntimeRecord) (StopResponse, error)) (bool, error) {
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return false, err
	}
	if err := ValidateDirectorySelection(role, canonical); err != nil {
		return false, err
	}
	paths := pathsFor(canonical)
	expected := NewInstanceID(CurrentAccountID(), role, canonical)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var record RuntimeRecord
	var response StopResponse
	active := false
	for {
		held, err := ProbeRuntimeLock(paths)
		if err != nil {
			return active, err
		}
		if !held {
			return active, nil
		}
		active = true
		record, err = ReadRuntimeRecord(paths.RuntimeRecord)
		if err == nil {
			if record.InstanceID != expected.String() || record.DataDir != canonical || record.Mode != role {
				return active, fmt.Errorf("the runtime record belongs to another instance")
			}
			response, err = request(ctx, record)
			if err == nil {
				break
			}
		}
		if !retryableControlPublication(err) {
			return active, fmt.Errorf("the instance did not accept the stop request: %w", err)
		}
		select {
		case <-ctx.Done():
			return active, fmt.Errorf("the instance did not accept the stop request before its wait ended: %w", errors.Join(ctx.Err(), err))
		case <-ticker.C:
		}
	}
	if !response.Accepted || response.AttemptID != record.AttemptID {
		return active, fmt.Errorf("the instance refused the stop request for this attempt")
	}
	if err := WaitForRuntimeRelease(ctx, paths); err != nil {
		return active, fmt.Errorf("shutdown could not be confirmed: %w", err)
	}
	return active, nil
}

func retryableControlPublication(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, ErrUnresponsive)
}
