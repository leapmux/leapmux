package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedCallbacks captures the order each callback fires in. The
// orchestration helpers (`runStartupPhase0`, `failStartup`) shape a
// specific sequence (label broadcast → mutation → rollback → persist
// → broadcast-failed → registry-fail) and the wrapper methods in
// agent.go / terminal.go rely on every step running in that order.
type recordedCallbacks struct {
	events []string
}

func (rc *recordedCallbacks) hooks() startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { rc.events = append(rc.events, "setMessage:"+label) },
		broadcastStarting: func(label string) { rc.events = append(rc.events, "broadcastStarting:"+label) },
		persistError:      func(errMsg string) { rc.events = append(rc.events, "persistError:"+errMsg) },
		broadcastFailed:   func(errMsg string) { rc.events = append(rc.events, "broadcastFailed:"+errMsg) },
		registryFail:      func(errMsg string) { rc.events = append(rc.events, "registryFail:"+errMsg) },
	}
}

// TestFailStartup_OrdersPersistBroadcastRegistry pins the contract
// that failStartup writes to the DB before broadcasting STARTUP_FAILED,
// and broadcasts before flipping the registry to its terminal state.
// Reversing any pair leaks an observable inconsistency: an observer
// could see the registry's FAILED status before the persisted
// startup_error column lands.
func TestFailStartup_OrdersPersistBroadcastRegistry(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	// Empty gitModeResult => no rollback path, only the failure tail.
	svc.failStartup(gitModeResult{}, errors.New("boom"), rc.hooks())

	require.Equal(t, []string{
		"persistError:boom",
		"broadcastFailed:boom",
		"registryFail:boom",
	}, rc.events, "failure tail must run in DB-then-broadcast-then-registry order")
}

// TestFailStartup_RollbackLabelBroadcastsBeforePersist verifies that a
// gitModeResult with a partial mutation triggers a rollback-label
// STARTING broadcast and the rollback itself before the failure tail.
// Without the rollback ordering the UI flashes STARTUP_FAILED before
// the user sees the "rolling back…" message.
func TestFailStartup_RollbackLabelBroadcastsBeforePersist(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	gm := gitModeResult{
		Rollback: gitModeRollback{CreatedBranch: &rollbackBranch{
			WorkingDir:    t.TempDir(),
			CreatedBranch: "feat",
		}},
	}
	svc.failStartup(gm, errors.New("boom"), rc.hooks())

	// First two events must be the rollback label broadcast; persist /
	// broadcast-failed / registry-fail follow in their fixed order.
	require.GreaterOrEqual(t, len(rc.events), 5)
	assert.Contains(t, rc.events[0], "setMessage:")
	assert.Contains(t, rc.events[1], "broadcastStarting:")
	assert.Equal(t, []string{
		"persistError:boom",
		"broadcastFailed:boom",
		"registryFail:boom",
	}, rc.events[len(rc.events)-3:])
}

// TestRunStartupPhase0_NoLabel_SkipsBroadcast confirms that a plan
// whose PhaseLabel() returns "" (the no-op / passthrough mode) executes
// the git-mode mutation without firing a STARTING broadcast — the
// frontend would otherwise see a spurious "" status flash.
func TestRunStartupPhase0_NoLabel_SkipsBroadcast(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	// A zero-value plan has Mode == 0 → default branch in PhaseLabel,
	// returning "". executeGitMode no-ops on this shape (no mutation
	// fields populated), matching the "Current" mode the dialogs send
	// when the user picks the default radio.
	plan := gitModePlan{WorkingDir: t.TempDir()}
	require.Equal(t, "", plan.PhaseLabel(), "test premise: zero-value plan has no label")

	_, err := svc.runStartupPhase0(context.Background(), plan, rc.hooks())
	require.NoError(t, err)
	assert.Empty(t, rc.events, "phase-0 must not broadcast when PhaseLabel is empty")
}
