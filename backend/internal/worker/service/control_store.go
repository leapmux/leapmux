package service

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"time"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// blockingControlRecord stores a live blocking control request in memory.
type blockingControlRecord struct {
	AgentID        string
	AgentSessionID string
	RequestID      string
	Payload        []byte
	SourceSeq      int64
	ClaimToken     string
	CreatedAt      time.Time
	publisher      any
}

func (r blockingControlRecord) toStored() storedControlRequest {
	return storedControlRequest{
		AgentID:        r.AgentID,
		AgentSessionID: r.AgentSessionID,
		RequestID:      r.RequestID,
		Payload:        r.Payload,
		SourceSeq:      r.SourceSeq,
		ClaimToken:     r.ClaimToken,
		NonBlocking:    false,
		CreatedAt:      r.CreatedAt,
	}
}

func storedControlFromDB(row db.ControlRequest) storedControlRequest {
	return storedControlRequest{
		AgentID:        row.AgentID,
		AgentSessionID: row.AgentSessionID,
		RequestID:      row.RequestID,
		Payload:        row.Payload,
		SourceSeq:      row.SourceSeq,
		ClaimToken:     row.ClaimToken,
		NonBlocking:    true,
		CreatedAt:      row.CreatedAt.Time,
	}
}

// storedControlRequest unifies in-memory blocking control requests and SQLite
// non-blocking control requests behind one read shape.
type storedControlRequest struct {
	AgentID        string
	AgentSessionID string
	RequestID      string
	Payload        []byte
	SourceSeq      int64
	ClaimToken     string
	NonBlocking    bool
	CreatedAt      time.Time
}

func cloneBlockingControlRecord(rec blockingControlRecord) blockingControlRecord {
	rec.Payload = slices.Clone(rec.Payload)
	return rec
}

// controlRequestStore owns in-memory blocking control requests and unifies
// read lookups across memory (blocking) and SQLite (non-blocking).
// The zero value is ready to use.
type controlRequestStore struct {
	mu       sync.RWMutex
	blocking map[string]map[string]blockingControlRecord // agentID -> requestID -> record
}

func (s *controlRequestStore) getBlocking(agentID, requestID string) (blockingControlRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byReq := s.blocking[agentID]
	if byReq == nil {
		return blockingControlRecord{}, false
	}
	rec, ok := byReq[requestID]
	if !ok {
		return blockingControlRecord{}, false
	}
	return cloneBlockingControlRecord(rec), true
}

func (s *controlRequestStore) hasBlocking(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.blocking[agentID]) > 0
}

func (s *controlRequestStore) rangeBlockingAgentIDs(fn func(agentID string)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id := range s.blocking {
		fn(id)
	}
}

func (s *controlRequestStore) hasBlockingInstance(agentID, requestID, claimToken string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byReq := s.blocking[agentID]
	if byReq == nil {
		return false
	}
	rec, ok := byReq[requestID]
	return ok && rec.ClaimToken == claimToken
}

func (s *controlRequestStore) upsertBlocking(rec blockingControlRecord) blockingControlRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocking == nil {
		s.blocking = make(map[string]map[string]blockingControlRecord)
	}
	byReq := s.blocking[rec.AgentID]
	if byReq == nil {
		byReq = make(map[string]blockingControlRecord)
		s.blocking[rec.AgentID] = byReq
	}
	rec = cloneBlockingControlRecord(rec)
	if existing, ok := byReq[rec.RequestID]; ok {
		if existing.ClaimToken == rec.ClaimToken && bytes.Equal(existing.Payload, rec.Payload) && existing.AgentSessionID == rec.AgentSessionID && existing.SourceSeq > 0 {
			rec.SourceSeq = existing.SourceSeq
		}
		if !existing.CreatedAt.IsZero() {
			rec.CreatedAt = existing.CreatedAt
		}
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowMillis()
	}
	byReq[rec.RequestID] = rec
	return cloneBlockingControlRecord(rec)
}

func (s *controlRequestStore) deleteBlocking(agentID, requestID, claimToken string) (blockingControlRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byReq := s.blocking[agentID]
	if byReq == nil {
		return blockingControlRecord{}, false
	}
	rec, ok := byReq[requestID]
	if !ok {
		return blockingControlRecord{}, false
	}
	if claimToken != "" && rec.ClaimToken != claimToken {
		return blockingControlRecord{}, false
	}
	delete(byReq, requestID)
	if len(byReq) == 0 {
		delete(s.blocking, agentID)
	}
	return cloneBlockingControlRecord(rec), true
}

func (s *controlRequestStore) drainBlocking(agentID string) []blockingControlRecord {
	s.mu.Lock()
	byReq := s.blocking[agentID]
	delete(s.blocking, agentID)
	s.mu.Unlock()
	if len(byReq) == 0 {
		return nil
	}
	out := make([]blockingControlRecord, 0, len(byReq))
	for _, rec := range byReq {
		out = append(out, cloneBlockingControlRecord(rec))
	}
	slices.SortFunc(out, func(a, b blockingControlRecord) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.RequestID, b.RequestID))
	})
	return out
}

func (s *controlRequestStore) drainBlockingForPublisher(agentID string, publisher any) []blockingControlRecord {
	s.mu.Lock()
	byReq := s.blocking[agentID]
	var out []blockingControlRecord
	for reqID, rec := range byReq {
		if rec.publisher == publisher {
			out = append(out, cloneBlockingControlRecord(rec))
			delete(byReq, reqID)
		}
	}
	if len(byReq) == 0 {
		delete(s.blocking, agentID)
	}
	s.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, func(a, b blockingControlRecord) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.RequestID, b.RequestID))
	})
	return out
}

// lookup resolves a pending control request from memory (blocking) or SQLite (non-blocking).
func (s *controlRequestStore) lookup(ctx context.Context, queries *db.Queries, agentID, requestID string) (storedControlRequest, bool, error) {
	if rec, ok := s.getBlocking(agentID, requestID); ok {
		return rec.toStored(), true, nil
	}
	row, err := queries.GetControlRequest(ctx, db.GetControlRequestParams{AgentID: agentID, RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) {
		return storedControlRequest{}, false, nil
	}
	if err != nil {
		return storedControlRequest{}, false, err
	}
	return storedControlFromDB(row), true, nil
}

// list returns all pending control requests for an agent across in-memory
// blocking requests and SQLite non-blocking requests, ordered by creation time.
func (s *controlRequestStore) list(ctx context.Context, queries *db.Queries, agentID string) ([]storedControlRequest, error) {
	rows, err := queries.ListControlRequestsByAgentID(ctx, agentID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	byReq := s.blocking[agentID]
	out := make([]storedControlRequest, 0, len(rows)+len(byReq))
	for _, rec := range byReq {
		out = append(out, cloneBlockingControlRecord(rec).toStored())
	}
	s.mu.RUnlock()
	for _, row := range rows {
		out = append(out, storedControlFromDB(row))
	}
	slices.SortFunc(out, func(a, b storedControlRequest) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.RequestID, b.RequestID))
	})
	return out, nil
}
