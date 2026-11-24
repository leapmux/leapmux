package semanticevents

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// TerminalFact copies the fields that control events presents. Screen output,
// geometry, Git metadata, and future fields do not enter the event journal.
func TerminalFact(event *leapmuxv1.TerminalEvent) *leapmuxv1.SemanticTabFact {
	fact := &leapmuxv1.SemanticTabFact{}
	switch value := event.GetEvent().(type) {
	case *leapmuxv1.TerminalEvent_StatusChange:
		status := value.StatusChange
		if status.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalStatus{TerminalStatus: &leapmuxv1.SemanticTerminalStatus{
			Status: status.GetStatus(), StartupError: status.GetStartupError(),
		}}
	case *leapmuxv1.TerminalEvent_Closed:
		if value.Closed == nil {
			return nil
		}
		closed := &leapmuxv1.TerminalClosed{}
		if value.Closed.ExitCode != nil {
			code := value.Closed.GetExitCode()
			closed.ExitCode = &code
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalExit{TerminalExit: closed}
	case *leapmuxv1.TerminalEvent_Bell:
		if value.Bell == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalBell{TerminalBell: &leapmuxv1.TerminalBell{}}
	case *leapmuxv1.TerminalEvent_Notification:
		if value.Notification == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalNotification{TerminalNotification: &leapmuxv1.TerminalNotification{
			Title: value.Notification.GetTitle(), Body: value.Notification.GetBody(),
		}}
	case *leapmuxv1.TerminalEvent_TitleChanged:
		if value.TitleChanged == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalTitle{TerminalTitle: &leapmuxv1.TerminalTitleChanged{
			Title: value.TitleChanged.GetTitle(),
		}}
	case *leapmuxv1.TerminalEvent_Progress:
		if value.Progress == nil {
			return nil
		}
		fact.Event = &leapmuxv1.SemanticTabFact_TerminalProgress{TerminalProgress: &leapmuxv1.TerminalProgress{
			State: value.Progress.GetState(), Percent: value.Progress.GetPercent(),
		}}
	default:
		return nil
	}
	return fact
}
