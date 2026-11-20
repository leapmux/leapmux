// Package eventjournal stores Worker notifications in publication order.
package eventjournal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const (
	maxPendingObservations = 256
	maxObservationBytes    = 64 << 10
	maxCommitBatch         = 32
	journalWriteTimeout    = 5 * time.Second
)

var (
	ErrUnavailable = errors.New("the Worker's event journal is unavailable")
	ErrCursor      = errors.New("the event checkpoint does not match retained history for this tab")
	ErrAdmission   = errors.New("no live tab with event history access")
)

// Journal owns the event tables in one Worker database. A failed commit or a
// full publication queue stops it before it can advertise a position past an
// unrecorded notification.
type Journal struct {
	store    store
	clock    quartz.Clock
	identity cursorIdentity
	queue    chan observation
	cancel   context.CancelFunc
	done     chan struct{}
	revoked  atomic.Bool

	mu                  sync.Mutex
	changed             chan struct{}
	accepted, committed uint64
	closing             bool
	readersStopped      bool
	failure             error
}

// Start binds the journal to the authoritative Worker owner.
func Start(ctx context.Context, database *sql.DB, workerID string, owner userid.UserID, clock quartz.Clock) (*Journal, error) {
	return start(ctx, database, workerID, owner, clock, defaultRetention())
}

func start(ctx context.Context, database *sql.DB, workerID string, owner userid.UserID, clock quartz.Clock, retention retention) (*Journal, error) {
	s := store{database: database, retention: retention}
	row, err := s.initialize(ctx, workerID, owner, clock.Now())
	if err != nil {
		return nil, fmt.Errorf("initialize event journal: %w", err)
	}
	writerCtx, cancel := context.WithCancel(context.Background())
	j := &Journal{
		store: s, clock: clock, identity: cursorIdentity{workerID: workerID, owner: owner, journalID: row.JournalID, key: row.CursorKey},
		queue: make(chan observation, maxPendingObservations), cancel: cancel, done: make(chan struct{}),
		changed: make(chan struct{}),
	}
	go j.writePublications(writerCtx)
	return j, nil
}

func (j *Journal) OwnedBy(owner userid.UserID) bool {
	return !j.revoked.Load() && j.identity.owner.MatchesUser(owner)
}

// Record serializes one immutable tab fact before admitting it. It never
// waits for the database or for a subscriber. Only committed facts are read.
func (j *Journal) Record(tab Tab, fact *leapmuxv1.SemanticTabFact) {
	if !tab.Valid() || fact.GetEvent() == nil {
		return
	}
	item := observation{tab: tab, observed: j.clock.Now().UnixMilli()}
	item.requestOpening = bytes.Clone(fact.GetAgentRequest().GetRequestInstance())
	var err error
	if proto.Size(fact) <= maxObservationBytes {
		item.fact, err = proto.Marshal(fact)
	}
	if err != nil || len(item.fact) == 0 {
		item.fact = []byte{}
		item.gap = leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT
	}
	j.enqueue(item)
}

// Seal orders retirement after facts already admitted for the resource.
// Call it after joining the publishers or after determining that their drain
// cannot be verified.
func (j *Journal) Seal(tabs []Tab, publishersFinished bool) {
	if len(tabs) == 0 {
		return
	}
	item := observation{observed: j.clock.Now().UnixMilli(), sealing: slices.Clone(tabs)}
	if !publishersFinished {
		item.gap = leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT
	}
	j.enqueue(item)
}

func (j *Journal) enqueue(item observation) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closing {
		return
	}
	select {
	case j.queue <- item:
		j.accepted++
	default:
		j.failLocked(errors.New("the event publication queue is full"))
	}
}

// Revoke ends readers and leaves the persisted dirty marker set. Use it when
// this process no longer has a single authoritative owner for its publishers.
func (j *Journal) Revoke() {
	j.revoked.Store(true)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.failLocked(errors.New("the Worker's owner changed"))
}

func (j *Journal) failLocked(err error) {
	if j.failure == nil {
		j.failure = err
	}
	j.closeAdmissionLocked()
	j.cancel()
}

func (j *Journal) closeAdmissionLocked() {
	if !j.closing {
		j.closing = true
		close(j.queue)
		j.signalLocked()
	}
}

func (j *Journal) signalLocked() {
	close(j.changed)
	j.changed = make(chan struct{})
}

// StopReaders ends subscriptions while admitted producers finish publishing.
func (j *Journal) StopReaders() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.readersStopped {
		j.readersStopped = true
		j.signalLocked()
	}
}

// Stop ends subscriptions and waits for admitted facts to commit. A restart
// reports a gap unless the producer teardown and the drain both finished.
func (j *Journal) Stop(ctx context.Context, producersStopped bool) error {
	j.mu.Lock()
	j.closeAdmissionLocked()
	j.mu.Unlock()
	defer j.cancel()
	select {
	case <-j.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failure != nil {
		return j.failure
	}
	if !producersStopped || j.committed != j.accepted {
		return nil
	}
	return db.New(j.store.database).SetSemanticEventJournalDirty(ctx, 0)
}

func (j *Journal) writePublications(ctx context.Context) {
	defer close(j.done)
	lastPrune := j.clock.Now()
	for first := range j.queue {
		if ctx.Err() != nil {
			return
		}
		batch := []observation{first}
		for len(batch) < maxCommitBatch && len(j.queue) > 0 {
			batch = append(batch, <-j.queue)
		}
		now := j.clock.Now()
		prune := now.Before(lastPrune) || now.Sub(lastPrune) >= time.Minute
		writeCtx, cancel := context.WithTimeout(ctx, journalWriteTimeout)
		err := j.store.write(writeCtx, func(q *db.Queries) error {
			for _, item := range batch {
				if len(item.sealing) > 0 {
					for _, tab := range item.sealing {
						item.tab = tab
						if err := j.store.seal(writeCtx, q, item); err != nil {
							return err
						}
					}
					continue
				}
				if _, err := j.store.append(writeCtx, q, item); err != nil {
					return err
				}
			}
			if prune {
				return j.store.pruneExpired(writeCtx, q, now)
			}
			return nil
		})
		cancel()
		if err != nil {
			j.mu.Lock()
			j.failLocked(fmt.Errorf("commit tab events: %w", err))
			j.mu.Unlock()
			return
		}
		if prune {
			lastPrune = now
		}
		j.mu.Lock()
		j.committed += uint64(len(batch))
		j.signalLocked()
		j.mu.Unlock()
	}
}

// Barrier captures the publications already admitted when a subscription
// opens. It does not wait for publications that enter afterwards.
func (j *Journal) barrier(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	target := j.accepted
	for !j.closing && !j.readersStopped && j.committed < target {
		changed := j.changed
		j.mu.Unlock()
		select {
		case <-ctx.Done():
			j.mu.Lock()
			return ctx.Err()
		case <-changed:
			j.mu.Lock()
		}
	}
	return j.checkAvailableLocked()
}

func (j *Journal) checkAvailableLocked() error {
	if j.closing || j.readersStopped {
		if j.failure != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, j.failure)
		}
		return ErrUnavailable
	}
	return nil
}
