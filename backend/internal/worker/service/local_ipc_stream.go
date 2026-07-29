package service

// LocalIPCStreamPrefix marks synthetic channel ids minted by the local
// IPC router. A handler whose sender.ChannelID() starts with this prefix
// has no E2EE channel behind it, so nothing keyed by channel id (today,
// the watcher registry) may assume the channel manager's close callback
// will ever fire for it -- see ReleaseLocalStream.
const LocalIPCStreamPrefix = "localipc:"

// ReleaseLocalStream retires everything a local-IPC stream owns: today,
// the event subscriptions it registered.
//
// It matters because local-IPC ids never reach the channel manager's close
// callback -- that fires for E2EE channels only -- and every local stream
// mints a FRESH synthetic id, so replace-semantics can never reclaim the
// previous one either. Without this, a `leapmux remote --follow` against an
// entity that then goes idle leaves a registration pinned for the life of the
// worker process, because only a failing broadcast would have swept it and no
// broadcast ever comes.
func (svc *Service) ReleaseLocalStream(streamID string) {
	if streamID == "" {
		return
	}
	if svc.Watchers != nil {
		svc.Watchers.UnwatchAll(streamID)
	}
}
