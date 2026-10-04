//go:build windows

package procutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsJobHeldHandleMatchesTheNativeCreationIdentity(t *testing.T) {
	child := startWaitingProcess(t)
	identity, found := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, found)
	held, err := (windowsJobDriver{}).OpenProcess(identity.PID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	current, runs, err := held.Identity()
	require.NoError(t, err)
	require.True(t, runs)
	assert.Equal(t, identity, current)
	require.True(t, child.endedByItself(t))
	ended, runs, err := held.Identity()
	require.NoError(t, err)
	require.False(t, runs)
	assert.Equal(t, identity, ended, "the held handle must retain the ended process's creation identity")
}

func TestWindowsJobRefusesChangedCreationIdentityAndKeepsTheProcessLive(t *testing.T) {
	child := startWaitingProcess(t)
	identity, found := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, found)
	changed := identity
	changed.StartTime++
	job, err := assignOwnedJob(changed, windowsJobDriver{})
	if job != 0 {
		t.Cleanup(func() { _ = (windowsJobDriver{}).CloseJob(job) })
	}
	require.Error(t, err)
	assert.Zero(t, job)
	assert.True(t, identity.Runs())
	require.True(t, child.endedByItself(t), "a rejected job must not acquire kill-on-close authority")
}

func TestWindowsJobEndsOnlyTheAssignedNativeProcess(t *testing.T) {
	first, second := startWaitingProcess(t), startWaitingProcess(t)
	identity, found := IdentifyProcess(first.cmd.Process.Pid)
	require.True(t, found)
	handle, err := assignOwnedJob(identity, windowsJobDriver{})
	require.NoError(t, err)
	job := &JobObject{}
	job.handle.Store(handle)
	t.Cleanup(func() { _ = job.Close() })
	require.NoError(t, job.Terminate())
	first.waitExit(t)
	assert.False(t, first.cmd.ProcessState.Success())
	require.True(t, second.endedByItself(t), "the second native process must remain outside the first job")
	require.NoError(t, job.Close())
}

func TestWindowsProcessOwnerEndsOnlyItsVerifiedNativeProcess(t *testing.T) {
	first, second := startWaitingProcess(t), startWaitingProcess(t)
	identity, found := IdentifyProcess(first.cmd.Process.Pid)
	require.True(t, found)
	owner, err := OwnStartedProcess(identity)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	require.Equal(t, identity.PID, owner.PID())
	require.NoError(t, owner.Terminate())
	first.waitExit(t)
	require.False(t, first.cmd.ProcessState.Success())
	require.True(t, second.endedByItself(t), "the owner must leave the second native process outside its job")
	require.NoError(t, owner.Close())
}
