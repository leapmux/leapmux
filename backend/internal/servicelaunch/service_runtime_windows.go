//go:build windows

package servicelaunch

import (
	"context"
	"fmt"

	"github.com/coder/quartz"
	"golang.org/x/sys/windows/svc"
)

// RunWindowsService enters SCM before the application reads configuration.
// The application callback owns startup and full graceful teardown.
func RunWindowsService(name string, application func(context.Context, func()) error) error {
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !service {
		return fmt.Errorf("the native service entry must run through the Windows Service Control Manager")
	}
	return svc.Run(name, &windowsServiceHandler{application: application})
}

type windowsServiceHandler struct {
	application func(context.Context, func()) error
}

func (h *windowsServiceHandler) Execute(_ []string, nativeRequests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan serviceLifecycleRequest)
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		defer close(requests)
		for {
			select {
			case <-ctx.Done():
				return
			case change, ok := <-nativeRequests:
				if !ok {
					return
				}
				var request serviceLifecycleRequest
				switch change.Cmd {
				case svc.Stop:
					request = serviceStop
				case svc.Shutdown:
					request = serviceShutdown
				case svc.Interrogate:
					request = serviceInterrogate
				default:
					continue
				}
				select {
				case requests <- request:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	supervisor := serviceSupervisor{
		application: h.application, requests: requests, clock: quartz.NewReal(),
		report: func(progress serviceProgress) {
			status := svc.Status{CheckPoint: progress.Checkpoint}
			switch progress.State {
			case serviceStarting:
				status.State = svc.StartPending
				status.WaitHint = 10000
			case serviceRunning:
				status.State = svc.Running
				status.Accepts = svc.AcceptStop | svc.AcceptShutdown
			case serviceStopping:
				status.State = svc.StopPending
				status.WaitHint = 10000
			case serviceStopped:
				return // svc.Run reports the final state with the returned exit code.
			}
			statuses <- status
		},
	}
	err := supervisor.run(ctx)
	cancel()
	<-bridgeDone
	if err != nil {
		return true, 1
	}
	return false, 0
}
