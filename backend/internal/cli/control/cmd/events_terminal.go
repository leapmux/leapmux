package cmd

import (
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func terminalFactEvent(fact *leapmuxv1.SemanticTabFact, tab scopedTab) (streamEvent, bool) {
	switch e := fact.GetEvent().(type) {
	case *leapmuxv1.SemanticTabFact_TerminalStatus:
		status := e.TerminalStatus.GetStatus()
		ev := tabStreamEvent(kindTerminalStatus, tab)
		ev.Status, ev.Error = enumToken(status), e.TerminalStatus.GetStartupError()
		ev.Summary = withError(tokenWords(ev.Status), ev.Error)
		return ev, true
	case *leapmuxv1.SemanticTabFact_TerminalExit:
		ev := tabStreamEvent(kindTerminalExit, tab)
		if e.TerminalExit.ExitCode == nil {
			// An unset code is an exit that the Worker holds no record of; a 0
			// would claim a clean exit that it never saw.
			ev.Summary = "the shell exited; the Worker holds no record of its exit code"
			return ev, true
		}
		code := e.TerminalExit.GetExitCode()
		ev.ExitCode = &code
		ev.Summary = fmt.Sprintf("the shell exited with code %d", code)
		return ev, true
	case *leapmuxv1.SemanticTabFact_TerminalBell:
		ev := tabStreamEvent(kindTerminalBell, tab)
		ev.Summary = "bell"
		return ev, true
	case *leapmuxv1.SemanticTabFact_TerminalNotification:
		ev := tabStreamEvent(kindTerminalNotification, tab)
		ev.Title, ev.Body = e.TerminalNotification.GetTitle(), e.TerminalNotification.GetBody()
		ev.Summary = ev.Body
		if ev.Title != "" {
			ev.Summary = ev.Title + ": " + ev.Body
		}
		return ev, true
	case *leapmuxv1.SemanticTabFact_TerminalTitle:
		title := e.TerminalTitle.GetTitle()
		ev := tabStreamEvent(kindTerminalTitle, tab)
		ev.Title, ev.Summary = title, fmt.Sprintf("%q", title)
		return ev, true
	case *leapmuxv1.SemanticTabFact_TerminalProgress:
		report := progressReport{State: enumToken(e.TerminalProgress.GetState()), Percent: e.TerminalProgress.GetPercent()}
		ev := tabStreamEvent(kindTerminalProgress, tab)
		ev.Progress, ev.Summary = &report, progressSummary(report)
		return ev, true
	default:
		return streamEvent{}, false
	}
}
