package procutil

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

type fixtureProcessExitSystem struct {
	watch      *processExitWatch
	openErr    error
	createdAt  int64
	createdErr error
	reads      int
	onCreated  func()
}

func (s *fixtureProcessExitSystem) open(int) (*processExitWatch, error) { return s.watch, s.openErr }
func (s *fixtureProcessExitSystem) created(context.Context, int) (int64, error) {
	s.reads++
	if s.onCreated != nil {
		s.onCreated()
	}
	return s.createdAt, s.createdErr
}

func TestProcessExitWatchKeepsIdentityAndReleasesOnEveryConstructionOutcome(t *testing.T) {
	identity := ProcessIdentity{PID: math.MaxInt32 - 1, StartTime: 7}
	failure := errors.New("native observation failed")
	for _, scenario := range []struct {
		label         string
		completed     bool
		pollErr       error
		created       int64
		createdErr    error
		wantErr       bool
		wantCompleted bool
		wantReads     int
		wantRelease   int
	}{
		{label: "current identity", created: 7, wantReads: 1},
		{label: "changed creation identity with a live watch", created: 8, wantErr: true, wantReads: 1, wantRelease: 1},
		{label: "native completed event", completed: true, wantCompleted: true},
		{label: "unknown completion", pollErr: failure, wantErr: true, wantRelease: 1},
		{label: "failed identity read", createdErr: failure, wantErr: true, wantReads: 1, wantRelease: 1},
		{label: "absent creation time", wantErr: true, wantReads: 1, wantRelease: 1},
	} {
		t.Run(scenario.label, func(t *testing.T) {
			releases := 0
			system := &fixtureProcessExitSystem{
				watch:     &processExitWatch{poll: func(time.Duration) (bool, error) { return scenario.completed, scenario.pollErr }, release: func() error { releases++; return nil }},
				createdAt: scenario.created, createdErr: scenario.createdErr,
			}
			watch, err := openProcessExitWatchWith(t.Context(), identity, system)
			if scenario.wantErr {
				require.Error(t, err)
				require.Nil(t, watch)
			} else {
				require.NoError(t, err)
				require.Equal(t, identity, watch.identity)
				assert.Equal(t, scenario.wantCompleted, watch.completed)
			}
			assert.Equal(t, scenario.wantReads, system.reads)
			assert.Equal(t, scenario.wantRelease, releases)
			if watch != nil {
				require.NoError(t, watch.close())
				require.NoError(t, watch.close())
				assert.Equal(t, 1, releases, "each native watch releases its resource once")
			}
		})
	}
}

func TestProcessExitWatchProvesExitDuringTheCreationRead(t *testing.T) {
	identity := ProcessIdentity{PID: math.MaxInt32 - 1, StartTime: 7}
	completed, polls, releases := false, 0, 0
	system := &fixtureProcessExitSystem{
		watch: &processExitWatch{
			poll:    func(time.Duration) (bool, error) { polls++; return completed, nil },
			release: func() error { releases++; return nil },
		},
		createdErr: errors.New("the process ended during the creation read"),
		onCreated:  func() { completed = true },
	}
	watch, err := openProcessExitWatchWith(t.Context(), identity, system)
	require.NoError(t, err, "the exact native exit event proves completion after the PID lookup fails")
	require.True(t, watch.completed)
	require.Equal(t, 2, polls)
	require.NoError(t, watch.close())
	require.Equal(t, 1, releases)
}

func TestProcessExitWaitRetainsCancellationDuringNativeObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watch := &processExitWatch{poll: func(time.Duration) (bool, error) { cancel(); return true, nil }}
	require.ErrorIs(t, watch.wait(ctx), context.Canceled)
}

func TestProcessExitWatchRetainsOpenAndReleaseErrors(t *testing.T) {
	identity := ProcessIdentity{PID: math.MaxInt32 - 1, StartTime: 7}
	openErr := errors.New("watch open failed")
	_, err := openProcessExitWatchWith(t.Context(), identity, &fixtureProcessExitSystem{openErr: openErr})
	require.ErrorIs(t, err, openErr)
	watch, err := openProcessExitWatchWith(t.Context(), identity, &fixtureProcessExitSystem{openErr: os.ErrProcessDone})
	require.NoError(t, err)
	require.True(t, watch.completed)
	require.NoError(t, watch.close())
	readErr, releaseErr := errors.New("identity read failed"), errors.New("watch release failed")
	_, err = openProcessExitWatchWith(t.Context(), identity, &fixtureProcessExitSystem{
		watch: &processExitWatch{poll: func(time.Duration) (bool, error) { return false, nil }, release: func() error { return releaseErr }}, createdErr: readErr,
	})
	require.ErrorIs(t, err, readErr)
	require.ErrorIs(t, err, releaseErr)
}

func TestProcessExitWatchRejectsInvalidIdentityAndContext(t *testing.T) {
	for _, identity := range []ProcessIdentity{{}, {PID: -1, StartTime: 1}, {PID: math.MaxInt32 + 1, StartTime: 1}, {PID: 1}, {PID: os.Getpid(), StartTime: 1}} {
		_, err := openProcessExitWatchWith(t.Context(), identity, &fixtureProcessExitSystem{})
		require.ErrorContains(t, err, "valid foreign process identity")
	}
	identity := ProcessIdentity{PID: math.MaxInt32 - 1, StartTime: 7}
	var absentContext context.Context
	_, err := openProcessExitWatchWith(absentContext, identity, &fixtureProcessExitSystem{})
	require.ErrorContains(t, err, "context is absent")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = openProcessExitWatchWith(ctx, identity, &fixtureProcessExitSystem{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestProcessExitWaitRetainsCancellationDeadlineAndNativeFailure(t *testing.T) {
	var absentContext context.Context
	watch := &processExitWatch{poll: func(time.Duration) (bool, error) {
		t.Fatal("a cancelled wait must not read the native watch")
		return false, nil
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, watch.wait(ctx), context.Canceled)
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	require.ErrorIs(t, watch.wait(ctx), context.DeadlineExceeded)
	require.ErrorContains(t, watch.wait(absentContext), "context is absent")
	failure := errors.New("native wait failed")
	watch.poll = func(time.Duration) (bool, error) { return false, failure }
	require.ErrorIs(t, watch.wait(t.Context()), failure)
}

func TestNativeProcessExitWatchObservesTheExactKilledProcess(t *testing.T) {
	first, second := startWaitingProcess(t), startWaitingProcess(t)
	identity, identified := IdentifyProcess(first.cmd.Process.Pid)
	require.True(t, identified)
	watch, err := openProcessExitWatch(testutil.DeadlineContext(t), identity)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, watch.close()) })
	require.False(t, watch.completed)
	sent, err := identity.Kill()
	require.NoError(t, err)
	require.True(t, sent)
	require.NoError(t, watch.wait(testutil.DeadlineContext(t)))
	first.waitExit(t)
	require.True(t, second.endedByItself(t), "the exit watch must not signal another process")
}

func TestProcessOwnerCloseSharesOneCompletedResult(t *testing.T) {
	child := startWaitingProcess(t)
	identity, identified := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, identified)
	owner, err := OwnStartedProcess(identity)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	failure := errors.New("completion receipt failed")
	var calls atomic.Int32
	owner.SetExitReceiptForTest(func(ctx context.Context, observed ProcessIdentity) error {
		if observed != identity {
			return errors.New("the completion receipt identifies another process")
		}
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	results := make(chan error, 2)
	go func() { results <- owner.Close() }()
	select {
	case <-entered:
	case result := <-results:
		t.Fatalf("Close returned before the completion receipt started: %v", result)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the owner did not wait for native completion")
	}
	go func() { results <- owner.Close() }()
	select {
	case <-results:
		t.Fatal("Close returned before the held completion receipt")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	readResult := func() error {
		select {
		case result := <-results:
			return result
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("Close did not return after the completion receipt")
			return nil
		}
	}
	first, second := readResult(), readResult()
	require.ErrorIs(t, first, failure)
	require.Same(t, first, second, "every Close caller receives the same completed result")
	require.EqualValues(t, 1, calls.Load())
	require.ErrorIs(t, owner.Err(), failure)
	child.waitExit(t)
}
