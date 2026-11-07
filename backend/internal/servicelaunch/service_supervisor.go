package servicelaunch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/util/errwrap"
)

// WindowsServiceEntryFlag selects the native dispatcher before application
// configuration. Native definitions supply it; foreground commands do not.
const WindowsServiceEntryFlag = "--native-service"

// WindowsServiceName derives the native name from the stable instance scope.
func WindowsServiceName(mode lifecycle.Role, instance string) string {
	return "leapmux-" + mode.String() + "-" + instance
}

type serviceLifecycleState string

const (
	serviceStarting serviceLifecycleState = "start-pending"
	serviceRunning  serviceLifecycleState = "running"
	serviceStopping serviceLifecycleState = "stop-pending"
	serviceStopped  serviceLifecycleState = "stopped"
)

type serviceLifecycleRequest string

const (
	serviceStop        serviceLifecycleRequest = "stop"
	serviceShutdown    serviceLifecycleRequest = "shutdown"
	serviceInterrogate serviceLifecycleRequest = "interrogate"
)

type serviceProgress struct {
	State      serviceLifecycleState
	Checkpoint uint32
}

type serviceSupervisor struct {
	application func(context.Context, func()) error
	requests    <-chan serviceLifecycleRequest
	report      func(serviceProgress)
	clock       quartz.Clock
}

const serviceProgressTick = "native-service-progress"

// run reports native readiness only after the application signal. Stop
// cancels the application and keeps reporting pending until its full join.
func (s serviceSupervisor) run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ready := make(chan struct{})
	var readyOnce sync.Once
	done := make(chan error, 1)
	ticker := s.clock.NewTicker(time.Second, serviceProgressTick)
	defer ticker.Stop()
	progress := serviceProgress{State: serviceStarting, Checkpoint: 1}
	s.report(progress)
	go func() { done <- s.application(ctx, func() { readyOnce.Do(func() { close(ready) }) }) }()
	parentDone := parent.Done()
	requests := s.requests
	var controlFailure error
	stop := func() {
		if progress.State == serviceStopping {
			return
		}
		cancel()
		progress = serviceProgress{State: serviceStopping, Checkpoint: 1}
		s.report(progress)
	}
	for {
		select {
		case err := <-done:
			s.report(serviceProgress{State: serviceStopped})
			if ctx.Err() != nil && errwrap.IsCancellation(err) {
				err = nil
			}
			if controlFailure != nil {
				return errors.Join(controlFailure, err)
			}
			return err
		case <-ready:
			ready = nil
			if progress.State == serviceStarting && ctx.Err() == nil {
				progress = serviceProgress{State: serviceRunning}
				s.report(progress)
			}
		case request, ok := <-requests:
			if !ok {
				requests = nil
				controlFailure = fmt.Errorf("the native service control channel closed")
				stop()
				continue
			}
			switch request {
			case serviceStop, serviceShutdown:
				stop()
			case serviceInterrogate:
				s.report(progress)
			}
		case <-parentDone:
			parentDone = nil
			stop()
		case <-ticker.C:
			if progress.State == serviceStarting || progress.State == serviceStopping {
				progress.Checkpoint++
				s.report(progress)
			}
		}
	}
}
