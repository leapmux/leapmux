package channelmgr

// channelBind is the ConnID protocol for useChannel.
// bindModeNone leaves ConnID untouched; bindModeConn reuses or writes connID.
type channelBind struct {
	mode   bindMode
	connID string
}

type bindMode int

const (
	bindModeNone bindMode = iota // UseChannelIf: never write ConnID
	bindModeConn                 // UseAuthorizedChannel: reuse or bind connID
)

func bindNone() channelBind { return channelBind{mode: bindModeNone} }

func bindConn(connID string) channelBind {
	return channelBind{mode: bindModeConn, connID: connID}
}

// bindPeek is the tagged RLock peek outcome for useChannel's ConnID protocol.
// Only ready carries an authorized ChannelInfo; reject and needWrite do not.
type bindPeek struct {
	kind bindPeekKind
	info ChannelInfo
}

type bindPeekKind int

const (
	bindPeekReject bindPeekKind = iota
	bindPeekReady
	bindPeekNeedWrite
)

func rejectBindPeek() bindPeek { return bindPeek{kind: bindPeekReject} }

func readyBindPeek(info ChannelInfo) bindPeek {
	return bindPeek{kind: bindPeekReady, info: info}
}

func needWriteBindPeek() bindPeek { return bindPeek{kind: bindPeekNeedWrite} }

// liveAndAuthorizeLocked snapshots a live channel and runs authorize. Caller
// holds m.mu (read or write). Shared by the RLock reuse path, the Lock bind
// path, and SendToFrontendIf so the live/auth gate cannot drift between them.
//
// authorize must not call Manager methods that take m.mu: under RLock a nested
// RLock can deadlock when a writer is waiting (sync.RWMutex writer preference),
// and under Lock any nested RLock/Lock deadlocks. Snapshot what you need from
// ChannelInfo instead.
func (m *Manager) liveAndAuthorizeLocked(
	channelID string,
	ch *channel,
	authorize func(ChannelInfo) bool,
) (ChannelInfo, bool) {
	if !m.channelLiveLocked(channelID, ch) {
		return ChannelInfo{}, false
	}
	info := channelInfo(ch)
	if authorize != nil && !authorize(info) {
		return ChannelInfo{}, false
	}
	return info, true
}

// peekBindDecision takes m.mu.RLock, decides whether ConnID already matches
// (authorize under RLock) or a write bind is required (defer authorize to Lock).
// Panic-safe: deferred RUnlock releases even if authorize panics.
func (m *Manager) peekBindDecision(
	channelID string,
	ch *channel,
	bind channelBind,
	authorize func(ChannelInfo) bool,
) bindPeek {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if bind.mode == bindModeNone || ch.ConnID == bind.connID {
		// Non-empty reuse still requires a registered user conn so UnbindUser
		// (no cleanup) cannot keep feeding FE→worker on a dead binding. Empty
		// ConnID equality is the Ready shortcut for holding opMu without
		// writing a binding.
		if bind.mode == bindModeConn && bind.connID != "" && !m.hasUserConnLocked(ch.UserID, bind.connID) {
			return rejectBindPeek()
		}
		info, ok := m.liveAndAuthorizeLocked(channelID, ch, authorize)
		if !ok {
			return rejectBindPeek()
		}
		return readyBindPeek(info)
	}
	if !m.channelLiveLocked(channelID, ch) {
		return rejectBindPeek()
	}
	return needWriteBindPeek()
}

// bindConnWithWriteLock takes m.mu.Lock, authorizes once, refuses a gone
// registered conn, and writes ConnID. Caller must have observed
// bindPeekNeedWrite (bind.mode == bindModeConn). Panic-safe: deferred Unlock
// releases even if authorize panics.
func (m *Manager) bindConnWithWriteLock(
	channelID string,
	ch *channel,
	bindConnID string,
	authorize func(ChannelInfo) bool,
) (ChannelInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.liveAndAuthorizeLocked(channelID, ch, authorize); !ok {
		return ChannelInfo{}, false
	}
	// Never write ConnID unless that user conn is currently registered. Covers
	// the RUnlock→Lock upgrade gap and any earlier Unbind that left the channel
	// alive because another user conn was still present.
	if !m.hasUserConnLocked(ch.UserID, bindConnID) {
		return ChannelInfo{}, false
	}
	if ch.ConnID != bindConnID {
		ch.ConnID = bindConnID
	}
	return channelInfo(ch), true
}

// hasUserConnLocked reports whether userID has a registered connID entry.
// Caller holds m.mu. Unlike getConnSender, this is presence-only and does not
// treat a nil SendFunc as "missing" — bind policy keys off registration.
func (m *Manager) hasUserConnLocked(userID, connID string) bool {
	conns := m.userSenders[userID]
	return conns != nil && conns[connID] != nil
}
