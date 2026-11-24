package providerkit

import "slices"

// CappedMap stores up to a fixed maximum number of entries and evicts the
// oldest keys in first-in, first-out (FIFO) order when full. Updating an
// existing key replaces its value without changing its eviction order.
// Callers must synchronize access.
type CappedMap[K comparable, V any] struct {
	limit   int
	entries map[K]V
	order   []K
}

// NewCappedMap returns a CappedMap that retains at most limit entries.
// A non-positive limit creates a map that stores no entries.
func NewCappedMap[K comparable, V any](limit int) CappedMap[K, V] {
	if limit < 0 {
		limit = 0
	}
	return CappedMap[K, V]{limit: limit}
}

// Cap returns the maximum number of entries the map retains.
func (m *CappedMap[K, V]) Cap() int {
	return m.limit
}

// Len returns the number of stored entries.
func (m *CappedMap[K, V]) Len() int {
	return len(m.entries)
}

// Get returns the value stored for key and whether key is present.
func (m *CappedMap[K, V]) Get(key K) (V, bool) {
	if len(m.entries) == 0 {
		var zero V
		return zero, false
	}
	value, ok := m.entries[key]
	return value, ok
}

// Has reports whether key is stored in the map.
func (m *CappedMap[K, V]) Has(key K) bool {
	if len(m.entries) == 0 {
		return false
	}
	_, ok := m.entries[key]
	return ok
}

// Set stores value under key. When key is already present, Set replaces its
// value without changing its position in the eviction order. When the map is
// at capacity, Set evicts the oldest key before storing the new one.
func (m *CappedMap[K, V]) Set(key K, value V) {
	if m.limit <= 0 {
		return
	}
	if m.entries == nil {
		m.entries = make(map[K]V)
	}
	if _, exists := m.entries[key]; exists {
		m.entries[key] = value
		return
	}
	if len(m.order) >= m.limit {
		oldest := m.order[0]
		delete(m.entries, oldest)
		copy(m.order, m.order[1:])
		m.order[len(m.order)-1] = key
	} else {
		m.order = append(m.order, key)
	}
	m.entries[key] = value
}

// Delete removes key from the map and from the eviction order.
func (m *CappedMap[K, V]) Delete(key K) {
	if len(m.entries) == 0 {
		return
	}
	if _, exists := m.entries[key]; !exists {
		return
	}
	delete(m.entries, key)
	if idx := slices.Index(m.order, key); idx >= 0 {
		var zero K
		copy(m.order[idx:], m.order[idx+1:])
		m.order[len(m.order)-1] = zero
		m.order = m.order[:len(m.order)-1]
	}
}

// DeleteFunc deletes every entry for which del returns true, visiting entries
// in insertion order and compacting the eviction slice in one pass.
func (m *CappedMap[K, V]) DeleteFunc(del func(K, V) bool) {
	if del == nil || len(m.entries) == 0 {
		return
	}
	kept := m.order[:0]
	for _, key := range m.order {
		if del(key, m.entries[key]) {
			delete(m.entries, key)
			continue
		}
		kept = append(kept, key)
	}
	clear(m.order[len(kept):])
	m.order = kept
}

// Reset removes every entry while keeping the configured limit.
func (m *CappedMap[K, V]) Reset() {
	clear(m.entries)
	clear(m.order)
	m.order = m.order[:0]
}
