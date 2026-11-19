package service

import "maps"

// registryWorkingEntry describes retained work and its counter contribution.
// A working activity marker contributes no task to the counter.
type registryWorkingEntry[K comparable] struct {
	key     K
	counted bool
}

// registryWorkingSet preserves constant-time activity and counter reads.
// The registry mutex guards every operation. The key map determines the count;
// include/remove update the count with the same mutation, and clone preserves both.
type registryWorkingSet[K comparable] struct {
	entries map[K]bool
	counted int32
}

func (set *registryWorkingSet[K]) include(key K, counted bool) {
	previous, present := set.entries[key]
	if present && previous == counted {
		return
	}
	if set.entries == nil {
		set.entries = make(map[K]bool)
	}
	set.entries[key] = counted
	if previous {
		set.counted--
	}
	if counted {
		set.counted++
	}
}

func (set *registryWorkingSet[K]) remove(key K) {
	if set.entries[key] {
		set.counted--
	}
	delete(set.entries, key)
}

func (set registryWorkingSet[K]) has(key K) bool   { _, present := set.entries[key]; return present }
func (set registryWorkingSet[K]) hasWorking() bool { return len(set.entries) > 0 }
func (set registryWorkingSet[K]) count() int32     { return set.counted }
func (set registryWorkingSet[K]) clone() registryWorkingSet[K] {
	return registryWorkingSet[K]{entries: maps.Clone(set.entries), counted: set.counted}
}

func (set *registryWorkingSet[K]) rename(oldKey, newKey K) {
	counted, present := set.entries[oldKey]
	if !present || oldKey == newKey {
		return
	}
	set.remove(oldKey)
	set.include(newKey, counted)
}
