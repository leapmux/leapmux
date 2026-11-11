package service

import (
	"errors"
	"sort"
	"sync"
)

var errArchiveObservationStale = errors.New("archive state changed during the Hub read; read it again")
var errArchiveObservationClosed = errors.New("the archive observation already finished")

// archiveObservationTracker orders authoritative reads without holding a lock
// over network or database work. Versions exist only while older reads need them.
type archiveObservationTracker struct {
	mu         sync.Mutex
	generation uint64
	active     map[*archiveObservation]uint64
	versions   map[string]uint64
}
type archiveObservation struct {
	tracker    *archiveObservationTracker
	generation uint64
}

func (t *archiveObservationTracker) begin() *archiveObservation {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		t.active = make(map[*archiveObservation]uint64)
		t.versions = make(map[string]uint64)
	}
	observation := &archiveObservation{tracker: t, generation: t.generation}
	t.active[observation] = observation.generation
	return observation
}

// accept runs under the resource's archive lock, before writing its flags.
// Even a same-value or failed write invalidates an older opposite observation.
func (o *archiveObservation) accept(keys []string) error {
	t := o.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, active := t.active[o]; !active {
		return errArchiveObservationClosed
	}
	for _, key := range keys {
		if t.versions[key] > o.generation {
			return errArchiveObservationStale
		}
	}
	if len(keys) == 0 {
		return nil
	}
	t.generation++
	for _, key := range keys {
		t.versions[key] = t.generation
	}
	return nil
}
func (o *archiveObservation) done() {
	t := o.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, o)
	oldest := t.generation
	for _, generation := range t.active {
		if generation < oldest {
			oldest = generation
		}
	}
	for key, generation := range t.versions {
		if generation <= oldest {
			delete(t.versions, key)
		}
	}
}

func (svc *Service) beginArchiveObservation() *archiveObservation {
	return svc.archiveObservations.begin()
}
func (tabs archiveTabSet) lockKeys() []string {
	keys := make([]string, 0, len(tabs.agents)+len(tabs.terminals)+len(tabs.payloads))
	for _, id := range tabs.agents {
		keys = append(keys, "agent:"+id)
	}
	for _, id := range tabs.terminals {
		keys = append(keys, "terminal:"+id)
	}
	for _, ref := range tabs.payloads {
		keys = append(keys, "payload:"+ref.userID+"\x00"+ref.tabID)
	}
	sort.Strings(keys)
	return keys
}
