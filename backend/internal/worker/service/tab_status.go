package service

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// tabStatusTextMaxBytes caps the text of a tab status. The text sits beside
// the tab's title in the tab bar and in the sidebar, so it is a short phrase.
// The Worker refuses a longer text rather than cutting it, so a script learns
// the limit instead of showing a text that it did not write. The browser
// truncates what it draws and needs no copy of the limit.
const tabStatusTextMaxBytes = 128

// tabStatusMaxProgress is the progress of finished work: TabStatus states its
// progress from 0 to 100.
const tabStatusMaxProgress = 100

const (
	defaultTabStatusKey  = "default"
	tabStatusKeyMaxBytes = 64
	tabStatusMaxKeys     = 32
)

// TabStatusStore holds the statuses that processes report for hosted tabs.
// The last entry in each tab's list supplies the visible status.
//
// It lives in memory only, and that is the contract SetTabStatus states: a
// status describes work that a running process reports, so it does not outlive
// the tab or the Worker. Every tab close clears it (applyTabCloseEffects), and
// the bootstrap of WatchWorkerPrivateEvents replays each status that is set
// (snapshotFor), so a frontend that opens later shows the same status as one
// that was open when the status was set.
//
// The statuses are grouped by owner, keyed by the owner's id as the bus keys
// its subscribers, because the events go to the owner's subscribers only and
// the bootstrap reads one owner's statuses. Within an owner, a tab is keyed by
// ownedTabKey, which normalizes the owner exactly as the close path does: an
// AGENT or a TERMINAL id is unique on its own, and a FILE or IMAGE id only
// within a user. The close path therefore clears a status with the key that it
// already builds, even when it does not know the owner (the orphan reconciler
// passes none for an agent or a terminal).
//
// Each change is published UNDER mu. Two concurrent writes to one tab would
// otherwise store A then B, but publish B then A, and every frontend would show
// the status that the store no longer holds. The bus publish never waits (see
// PrivateEventsBus.publish), so the hold is short, and the lock order is always
// mu, then the bus lock: snapshotFor runs with the bus lock released.
type TabStatusStore struct {
	events *PrivateEventsBus

	mu      sync.Mutex
	byOwner map[string]*ownerTabStatuses
}

// ownerTabStatuses is the statuses of one user's tabs. An entry exists only
// while it holds a status, so the map cannot grow with users who cleared
// theirs.
type ownerTabStatuses struct {
	owner userid.UserID
	tabs  map[ownedTabKey][]*leapmuxv1.TabStatusEntry
}

// NewTabStatusStore returns an empty store that publishes its changes on
// events.
func NewTabStatusStore(events *PrivateEventsBus) *TabStatusStore {
	return &TabStatusStore{
		events:  events,
		byOwner: make(map[string]*ownerTabStatuses),
	}
}

// set stores a status under one key and publishes the new visible status.
// The store and its events share the validated status. It must stay immutable.
//
// An unminted owner stores nothing. No subscriber could read the status: the
// bus refuses to register or publish to such an owner.
func (s *TabStatusStore) set(key ownedTabKey, owner userid.UserID, statusKey string, status *leapmuxv1.TabStatus) error {
	if owner.IsZero() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses, ok := s.byOwner[owner.String()]
	if !ok {
		statuses = &ownerTabStatuses{owner: owner, tabs: make(map[ownedTabKey][]*leapmuxv1.TabStatusEntry)}
		s.byOwner[owner.String()] = statuses
	}
	entries := statuses.tabs[key]
	if index := tabStatusEntryIndex(entries, statusKey); index >= 0 {
		entries = slices.Delete(entries, index, index+1)
	} else if len(entries) == tabStatusMaxKeys {
		return fmt.Errorf("a tab can hold at most %d status keys", tabStatusMaxKeys)
	}
	statuses.tabs[key] = append(entries, &leapmuxv1.TabStatusEntry{Key: statusKey, Status: status})
	s.events.PublishTabStatusChanged(owner, key.tabType, key.tabID, status)
	return nil
}

// clear removes one key and publishes the remaining visible status.
//
// It publishes even when no status is set. The bus drops an event for a slow
// subscriber, so a frontend can still show a status that the store no longer
// holds, and a repeated clear is how a script makes that frontend agree.
func (s *TabStatusStore) clear(key ownedTabKey, owner userid.UserID, statusKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []*leapmuxv1.TabStatusEntry
	if statuses, ok := s.byOwner[owner.String()]; ok {
		entries = statuses.tabs[key]
		if index := tabStatusEntryIndex(entries, statusKey); index >= 0 {
			entries = slices.Delete(entries, index, index+1)
			if len(entries) == 0 {
				s.removeLocked(statuses, key)
			} else {
				statuses.tabs[key] = entries
			}
		}
	}
	s.events.PublishTabStatusChanged(owner, key.tabType, key.tabID, latestTabStatus(entries))
}

func tabStatusEntryIndex(entries []*leapmuxv1.TabStatusEntry, statusKey string) int {
	return slices.IndexFunc(entries, func(entry *leapmuxv1.TabStatusEntry) bool { return entry.GetKey() == statusKey })
}

func latestTabStatus(entries []*leapmuxv1.TabStatusEntry) *leapmuxv1.TabStatus {
	if len(entries) == 0 {
		return nil
	}
	return entries[len(entries)-1].GetStatus()
}

// list gives one owner's entries from most to least recently set.
func (s *TabStatusStore) list(key ownedTabKey, owner userid.UserID) []*leapmuxv1.TabStatusEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	statuses := s.byOwner[owner.String()]
	if statuses == nil {
		return nil
	}
	entries := slices.Clone(statuses.tabs[key])
	slices.Reverse(entries)
	return entries
}

// forgetClosed removes the status of a tab that closed, and tells the owner's
// frontends when the tab had one. A tab without a status publishes nothing:
// every close of every tab reaches this, and nothing changed for a frontend.
//
// It looks for the key under every owner, because the close path does not
// always know the owner. A Worker serves one user, so that is one map lookup.
func (s *TabStatusStore) forgetClosed(key ownedTabKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, statuses := range s.byOwner {
		if _, ok := statuses.tabs[key]; !ok {
			continue
		}
		s.removeLocked(statuses, key)
		s.events.PublishTabStatusChanged(statuses.owner, key.tabType, key.tabID, nil)
	}
}

// removeLocked deletes one status of statuses, and the owner's entry with it
// when that was the last one. The caller holds mu.
func (s *TabStatusStore) removeLocked(statuses *ownerTabStatuses, key ownedTabKey) {
	delete(statuses.tabs, key)
	if len(statuses.tabs) == 0 {
		delete(s.byOwner, statuses.owner.String())
	}
}

// get returns the status of the tab that key identifies, or nil when none is
// set.
func (s *TabStatusStore) get(key ownedTabKey) *leapmuxv1.TabStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, statuses := range s.byOwner {
		if entries, ok := statuses.tabs[key]; ok {
			return latestTabStatus(entries)
		}
	}
	return nil
}

// snapshotFor returns one TabStatusChanged for each of owner's tabs whose
// status is set, in tab id order, for the bootstrap of
// WatchWorkerPrivateEvents. An unminted owner owns nothing.
//
// It takes only the store lock, never the bus lock, which is what lets
// SnapshotAndSubscribe call it: its snapshotFn must not reenter the bus.
func (s *TabStatusStore) snapshotFor(owner userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
	if owner.IsZero() {
		return nil
	}
	s.mu.Lock()
	var tabs []tabStatusSnapshotEntry
	if statuses, ok := s.byOwner[owner.String()]; ok {
		tabs = make([]tabStatusSnapshotEntry, 0, len(statuses.tabs))
		for key, entries := range statuses.tabs {
			tabs = append(tabs, tabStatusSnapshotEntry{tabType: key.tabType, tabID: key.tabID, status: latestTabStatus(entries)})
		}
	}
	s.mu.Unlock()
	// Map iteration order is random. A stable order keeps the bootstrap the
	// same from one subscription to the next.
	slices.SortFunc(tabs, func(a, b tabStatusSnapshotEntry) int { return cmp.Compare(a.tabID, b.tabID) })
	events := make([]*leapmuxv1.WorkerPrivateEvent, 0, len(tabs))
	for _, tab := range tabs {
		events = append(events, tabStatusChangedEvent(tab.tabType, tab.tabID, tab.status))
	}
	return events
}

// tabStatusSnapshotEntry is one status that snapshotFor copied out of the
// store.
type tabStatusSnapshotEntry struct {
	tabType leapmuxv1.TabType
	tabID   string
	status  *leapmuxv1.TabStatus
}

func validTabStatusKey(requested *string) (string, error) {
	if requested == nil {
		return defaultTabStatusKey, nil
	}
	key := strings.TrimSpace(*requested)
	if key == "" {
		return "", errors.New("the status key is empty")
	}
	if len(key) > tabStatusKeyMaxBytes {
		return "", fmt.Errorf("the status key exceeds %d bytes", tabStatusKeyMaxBytes)
	}
	if strings.ContainsFunc(key, unicode.IsControl) {
		return "", errors.New("the status key contains a control character")
	}
	return key, nil
}

// validTabStatus checks a status that a caller sent and returns the status to
// store: a new message that holds the trimmed text and the caller's progress
// and tone. A new message, so the store shares nothing with the request, and
// so no field that a newer client added reaches the frontends unchecked.
func validTabStatus(in *leapmuxv1.TabStatus) (*leapmuxv1.TabStatus, error) {
	text := strings.TrimSpace(in.GetText())
	if text == "" {
		return nil, errors.New("status.text is required")
	}
	if len(text) > tabStatusTextMaxBytes {
		return nil, fmt.Errorf("status.text is %d bytes long; the limit is %d bytes", len(text), tabStatusTextMaxBytes)
	}
	out := &leapmuxv1.TabStatus{Text: text}
	if in.ProgressPercent != nil {
		progress := in.GetProgressPercent()
		if progress > tabStatusMaxProgress {
			return nil, fmt.Errorf("status.progress_percent is %d; it must be from 0 to %d", progress, tabStatusMaxProgress)
		}
		out.ProgressPercent = &progress
	}
	// An open proto3 enum carries any number. A tone that this Worker does not
	// know would reach every frontend as a value that none of them can draw.
	tone := in.GetTone()
	if _, known := leapmuxv1.TabStatusTone_name[int32(tone)]; !known {
		return nil, fmt.Errorf("status.tone %d is not a tone that this worker knows", tone)
	}
	out.Tone = tone
	return out, nil
}
