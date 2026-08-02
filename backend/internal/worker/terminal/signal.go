package terminal

// SignalKind is one notification-class thing the tracker saw in PTY output.
type SignalKind uint8

const (
	SignalBell SignalKind = iota
	SignalNotification
	SignalTitle
	SignalProgress
)

// ProgressState is the ConEmu / Windows Terminal progress protocol state.
type ProgressState uint8

const (
	ProgressClear ProgressState = iota
	ProgressNormal
	ProgressError
	ProgressIndeterminate
	ProgressPaused
)

// Signal is one such observation. The tracker keeps no history: the enclosing
// ScreenBuffer drains the slice on every Write and the caller broadcasts it.
type Signal struct {
	Kind    SignalKind
	Title   string // SignalNotification, SignalTitle
	Body    string // SignalNotification
	State   ProgressState
	Percent int32
}
