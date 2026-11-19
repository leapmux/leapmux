//go:build windows

package procutil

import (
	"context"
	"fmt"
	"math"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
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
	members, err := readWindowsJobMembers(t.Context(), windows.Handle(handle))
	require.NoError(t, err)
	assert.Equal(t, []ProcessIdentity{identity}, members)
	outsider, live, err := identifyWindowsJobMember(windows.Handle(handle), uint32(second.cmd.Process.Pid))
	require.NoError(t, err)
	assert.False(t, live, "a live process outside the queried job grants no ownership")
	assert.Zero(t, outsider)
	require.NoError(t, job.Terminate())
	first.waitExit(t)
	assert.False(t, first.cmd.ProcessState.Success())
	require.True(t, second.endedByItself(t), "the second native process must remain outside the first job")
	require.NoError(t, job.Close())
}

func TestWindowsJobProcessListGrowsToTheReportedCount(t *testing.T) {
	const members = 25
	want := make([]uintptr, members)
	for index := range want {
		want[index] = uintptr(index + 345)
	}
	queries := 0
	pids, err := readJobProcessIDs(t.Context(), func(buffer []uintptr) error {
		queries++
		header := (*jobProcessListHeader)(unsafe.Pointer(&buffer[0]))
		header.assigned = members
		n := copy(buffer[jobProcessHeaderWords:], want)
		header.returned = uint32(n)
		if n < members {
			return windows.ERROR_MORE_DATA
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, queries)
	assert.Equal(t, want, pids)
}

func TestWindowsJobProcessListKeepsReturnedIDsWhenProcessesExit(t *testing.T) {
	const livePID = uintptr(2345)
	for _, returned := range []bool{false, true} {
		t.Run(fmt.Sprintf("member remains=%t", returned), func(t *testing.T) {
			var want []uintptr
			if returned {
				want = []uintptr{livePID}
			}
			pids, err := readJobProcessIDs(t.Context(), func(buffer []uintptr) error {
				header := (*jobProcessListHeader)(unsafe.Pointer(&buffer[0]))
				header.returned = uint32(copy(buffer[jobProcessHeaderWords:], want))
				header.assigned = header.returned + 1
				return nil
			})
			require.NoError(t, err)
			assert.ElementsMatch(t, want, pids)
		})
	}
}

func TestWindowsJobProcessListGrowsWithNativeMembers(t *testing.T) {
	driver := windowsJobDriver{}
	handle, err := driver.CreateJob()
	require.NoError(t, err)
	job := &JobObject{}
	job.handle.Store(handle)
	t.Cleanup(func() { _ = job.Close() })
	require.NoError(t, driver.ConfigureJob(handle))

	const processCount = 17
	want := make([]ProcessIdentity, 0, processCount)
	for range processCount {
		child := startWaitingProcess(t)
		identity, found := IdentifyProcess(child.cmd.Process.Pid)
		require.True(t, found)
		held, err := driver.OpenProcess(identity.PID)
		require.NoError(t, err)
		assignErr := held.Assign(handle)
		closeErr := held.Close()
		require.NoError(t, assignErr)
		require.NoError(t, closeErr)
		want = append(want, identity)
	}
	members, err := readWindowsJobMembers(t.Context(), windows.Handle(handle))
	require.NoError(t, err)
	assert.ElementsMatch(t, want, members)
	require.NoError(t, job.Terminate())
}

func TestWindowsJobProcessListRejectsInvalidNativeData(t *testing.T) {
	t.Run("count outside the buffer", func(t *testing.T) {
		pids, err := readJobProcessIDs(t.Context(), func(buffer []uintptr) error {
			header := (*jobProcessListHeader)(unsafe.Pointer(&buffer[0]))
			header.assigned = uint32(len(buffer))
			header.returned = header.assigned
			return nil
		})
		require.ErrorContains(t, err, "invalid process count")
		assert.Nil(t, pids)
	})
	t.Run("buffer size overflow", func(t *testing.T) {
		pids, err := readJobProcessIDs(t.Context(), func(buffer []uintptr) error {
			header := (*jobProcessListHeader)(unsafe.Pointer(&buffer[0]))
			header.assigned = math.MaxUint32
			return windows.ERROR_MORE_DATA
		})
		require.ErrorContains(t, err, "exceeds the system buffer limit")
		assert.Nil(t, pids)
	})
	t.Run("unexpected system error", func(t *testing.T) {
		pids, err := readJobProcessIDs(t.Context(), func([]uintptr) error { return windows.ERROR_ACCESS_DENIED })
		require.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED)
		assert.Nil(t, pids)
	})
	t.Run("canceled between native queries", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		queries := 0
		pids, err := readJobProcessIDs(ctx, func([]uintptr) error {
			queries++
			cancel()
			return windows.ERROR_MORE_DATA
		})
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, queries)
		assert.Nil(t, pids)
	})
	t.Run("canceled during the final query", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		pids, err := readJobProcessIDs(ctx, func([]uintptr) error {
			cancel()
			return nil
		})
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, pids)
	})
	t.Run("missing required buffer capacity", func(t *testing.T) {
		pids, err := readJobProcessIDs(t.Context(), func([]uintptr) error { return windows.ERROR_MORE_DATA })
		require.ErrorContains(t, err, "incomplete process list")
		assert.Nil(t, pids)
	})
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
