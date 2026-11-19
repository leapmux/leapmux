package dirac

import "github.com/coder/quartz"

// beforeWaitCleanup joins the final native read before generic child cleanup.
func (a *Agent) beforeWaitCleanup() {
	a.archiveWG.Wait()
}

func (a *Agent) archiveClock() quartz.Clock {
	if a.clock != nil {
		return a.clock
	}
	return quartz.NewReal()
}

// clearChildState stops readers of the outgoing native session. An already
// captured output keeps its original destination through the transition.
func (a *Agent) clearChildState() {
	a.childMu.Lock()
	if a.childStop != nil {
		close(a.childStop)
		a.childStop = nil
	}
	a.taskletWatch = nil
	a.childMu.Unlock()
}
