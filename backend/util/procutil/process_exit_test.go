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

type processExitObserverFunc func(context.Context, ProcessIdentity, error) error

func (observe processExitObserverFunc) ObserveExit(ctx context.Context, identity ProcessIdentity, nativeErr error) error {
	return observe(ctx, identity, nativeErr)
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
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	failure := errors.New("completion receipt failed")
	var calls atomic.Int32
	observer := processExitObserverFunc(func(ctx context.Context, observed ProcessIdentity, nativeErr error) error {
		assert.NoError(t, nativeErr)
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
	owner, err := OwnStartedProcessWithExitObserver(identity, observer)
	require.NoError(t, err)
	results := make(chan error, 2)
	go func() { results <- owner.Close() }()
	select {
	case <-entered:
	case result := <-results:
		t.Fatalf("Close returned before the completion receipt started: %v", result)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the owner did not wait for native completion")
	}
	completed, err := openProcessExitWatch(testutil.DeadlineContext(t), identity)
	require.NoError(t, err)
	require.True(t, completed.completed, "the native watch must prove exit before the observer returns its error")
	require.NoError(t, completed.close())
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
	require.NoError(t, owner.Capture(t.Context()))
	require.Same(t, first, owner.Err(), "capture after completion must keep the immutable termination result")
	child.waitExit(t)
}

func TestProcessOwnerExitObserverPermitsStateReads(t *testing.T) {
	child := startWaitingProcess(t)
	identity, identified := IdentifyProcess(child.cmd.Process.Pid)
	require.True(t, identified)
	var owner *ProcessOwner
	stateReadErr := errors.New("process owner state reads did not complete before the exit observer")
	observer := processExitObserverFunc(func(ctx context.Context, observed ProcessIdentity, nativeErr error) error {
		if nativeErr != nil {
			return nil
		}
		if observed != identity {
			return errors.New("the exit observer received another creation identity")
		}
		type stateRead struct {
			pid int
			err error
		}
		read := make(chan stateRead, 1)
		go func() { read <- stateRead{pid: owner.PID(), err: owner.Err()} }()
		select {
		case result := <-read:
			if result.pid != identity.PID {
				return errors.New("the owner state changed its captured process identity")
			}
			return result.err
		case <-ctx.Done():
			return stateReadErr
		}
	})
	owner, err := OwnStartedProcessWithExitObserver(identity, observer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Close() })
	require.NoError(t, owner.Close(), "the exit observer must read owner state without a mutex deadlock")
	child.waitExit(t)
}

func TestProcessExitObservationRetainsNativeObserverAndResourceErrors(t *testing.T) {
	nativeFailure := errors.New("native observation failed")
	observerFailure := errors.New("the exit observer failed")
	resourceFailure := errors.New("the native watch release failed")
	identity := ProcessIdentity{PID: math.MaxInt32 - 1, StartTime: 7}
	for _, scenario := range []struct {
		name           string
		nativeErr      error
		observerErr    error
		releaseErr     error
		absentObserver bool
	}{
		{name: "successful observation"},
		{name: "native error with successful observer", nativeErr: nativeFailure},
		{name: "observer error after native completion", observerErr: observerFailure},
		{name: "all errors", nativeErr: nativeFailure, observerErr: observerFailure, releaseErr: resourceFailure},
		{name: "absent observer", nativeErr: nativeFailure, releaseErr: resourceFailure, absentObserver: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var order []string
			watch := &processExitWatch{
				identity: identity,
				poll: func(time.Duration) (bool, error) {
					order = append(order, "native")
					return scenario.nativeErr == nil, scenario.nativeErr
				},
				release: func() error {
					order = append(order, "release")
					return scenario.releaseErr
				},
			}
			var observer ProcessExitObserver
			if !scenario.absentObserver {
				observer = processExitObserverFunc(func(_ context.Context, observed ProcessIdentity, nativeErr error) error {
					order = append(order, "observer")
					assert.Equal(t, identity, observed)
					assert.Equal(t, scenario.nativeErr, nativeErr)
					assert.Equal(t, scenario.nativeErr == nil, watch.completed)
					return scenario.observerErr
				})
			}
			err := observeProcessExit(t.Context(), watch, observer)
			for _, cause := range []error{scenario.nativeErr, scenario.observerErr, scenario.releaseErr} {
				if cause != nil {
					require.ErrorIs(t, err, cause)
				}
			}
			if scenario.nativeErr == nil && scenario.observerErr == nil && scenario.releaseErr == nil {
				require.NoError(t, err)
			}
			if scenario.absentObserver {
				assert.Equal(t, []string{"native", "release"}, order)
			} else {
				assert.Equal(t, []string{"native", "observer", "release"}, order)
			}
			beforeClose := append([]string(nil), order...)
			assert.Equal(t, scenario.releaseErr, watch.close())
			assert.Equal(t, beforeClose, order, "a repeated close must not release the native watch again")
		})
	}
	var absentContext context.Context
	require.ErrorContains(t, observeProcessExit(absentContext, nil, nil), "watch is absent")
}
