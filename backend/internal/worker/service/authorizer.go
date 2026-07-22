package service

import "strings"

// LocalIPCStreamPrefix marks synthetic channel ids minted by the local
// IPC router. A handler whose sender.ChannelID() starts with this prefix
// has no E2EE channel; its workspace access must be resolved through
// the LocalIPCAuthorizer registry below.
const LocalIPCStreamPrefix = "localipc:"

// WorkspaceAuthorizer abstracts the "is this workspace accessible to the
// caller?" check so handlers can be invoked over both E2EE channels
// (channel.ResponseWriter carries a channel id; AccessibleWorkspaceIDs comes
// from the channelmgr) and local IPC (per-token scope mapped at
// authentication time, no channel id).
//
// Use AuthorizerFor to pick the right implementation per request.
type WorkspaceAuthorizer interface {
	// IsAccessible reports whether the caller may operate on workspaceID.
	IsAccessible(workspaceID string) bool
	// AccessibleSet returns a defensive copy of the accessible-workspace
	// set. Callers iterate the result during list-style handler bulk
	// filtering; returning a copy prevents the caller from mutating the
	// authorizer's backing map. Returns nil when no workspaces are
	// scoped (matches `IsAccessible -> false for all`).
	AccessibleSet() map[string]bool
}

// copyAccessibleSet returns a fresh map[string]bool with the same
// entries as `src` so the authorizer's backing map stays
// caller-immutable.
func copyAccessibleSet(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// channelAuthorizer adapts the existing channel-based check.
type channelAuthorizer struct {
	channelID string
	mgr       channelManagerLike
}

type channelManagerLike interface {
	AccessibleWorkspaceIDs(channelID string) map[string]bool
	// IsWorkspaceAccessible is the per-RPC membership check; prefer it over
	// AccessibleWorkspaceIDs for a single-key test so the access gates do not
	// allocate and copy the whole set on every request.
	IsWorkspaceAccessible(channelID, workspaceID string) bool
}

func (c *channelAuthorizer) IsAccessible(workspaceID string) bool {
	if c.channelID == "" {
		return false
	}
	return c.mgr.IsWorkspaceAccessible(c.channelID, workspaceID)
}

func (c *channelAuthorizer) AccessibleSet() map[string]bool {
	if c.channelID == "" {
		return nil
	}
	return copyAccessibleSet(c.mgr.AccessibleWorkspaceIDs(c.channelID))
}

// AuthorizerFor returns a WorkspaceAuthorizer matched to the transport
// that channelID came from. Synthetic local-IPC channel ids consult the
// per-Service registry; everything else falls back to the channel
// manager.
//
// It takes the id rather than the writer it used to be read off: the
// authorizer never writes, and the narrower parameter lets callers that
// have no send-capable writer -- tests, and the local-IPC path -- ask
// the same question.
func (svc *Service) AuthorizerFor(cid string) WorkspaceAuthorizer {
	if strings.HasPrefix(cid, LocalIPCStreamPrefix) {
		if auth := svc.localAuthorizerFor(cid); auth != nil {
			return auth
		}
		// No registration: deny by returning an empty authorizer. The
		// router is supposed to register before dispatch and unregister
		// after; missing entries are a programming error, but we fail
		// closed to be safe.
		return &LocalIPCAuthorizer{StreamID: cid}
	}
	return &channelAuthorizer{channelID: cid, mgr: svc.Channels}
}

// LocalIPCAuthorizer is a static authorizer used by the worker's local
// IPC server. It's populated from the spawned-agent token's scope
// (workspace_id) at authentication time.
type LocalIPCAuthorizer struct {
	WorkspaceIDs map[string]bool
	StreamID     string
}

func (l *LocalIPCAuthorizer) IsAccessible(workspaceID string) bool {
	if l == nil {
		return false
	}
	return l.WorkspaceIDs[workspaceID]
}

func (l *LocalIPCAuthorizer) AccessibleSet() map[string]bool {
	if l == nil {
		return nil
	}
	return copyAccessibleSet(l.WorkspaceIDs)
}

// RegisterLocalAuthorizer stashes a per-stream authorizer for the
// duration of one local-IPC request or stream. The router calls this at
// dispatch entry; ReleaseLocalStream at exit.
func (svc *Service) RegisterLocalAuthorizer(streamID string, workspaceIDs []string) {
	if streamID == "" {
		return
	}
	set := make(map[string]bool, len(workspaceIDs))
	for _, w := range workspaceIDs {
		if w != "" {
			set[w] = true
		}
	}
	svc.localAuthorizers.Store(streamID, &LocalIPCAuthorizer{
		StreamID:     streamID,
		WorkspaceIDs: set,
	})
}

// ReleaseLocalStream retires everything a local-IPC stream owns: its
// authorizer and any event subscriptions it registered.
//
// The watcher half matters because local-IPC ids never reach the channel
// manager's close callback -- that fires for E2EE channels only -- and
// every local stream mints a FRESH synthetic id, so replace-semantics can
// never reclaim the previous one either. Without this, a `leapmux remote
// --follow` against an entity that then goes idle leaves a registration
// pinned for the life of the worker process, because only a failing
// broadcast would have swept it and no broadcast ever comes.
func (svc *Service) ReleaseLocalStream(streamID string) {
	if streamID == "" {
		return
	}
	svc.localAuthorizers.Delete(streamID)
	if svc.Watchers != nil {
		svc.Watchers.UnwatchAll(streamID)
	}
}

// localAuthorizerFor looks up the authorizer for streamID, or nil.
func (svc *Service) localAuthorizerFor(streamID string) *LocalIPCAuthorizer {
	v, ok := svc.localAuthorizers.Load(streamID)
	if !ok {
		return nil
	}
	auth, _ := v.(*LocalIPCAuthorizer)
	return auth
}
