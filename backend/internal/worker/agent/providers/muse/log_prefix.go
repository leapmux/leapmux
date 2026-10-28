package muse

import "fmt"

// All log helpers share the caller's stateMu with native record admission.
func (l *nativeLog) signalNativeChange() {
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

func (l *nativeLog) invalidateNativeRecord(id string) {
	l.invalid[id] = true
	if record, exists := l.records[id]; exists {
		sequence := record.record.Sequence
		if l.invalidPrefixFrom == 0 || sequence < l.invalidPrefixFrom {
			l.invalidPrefixFrom = sequence
		}
	}
}

func (l *nativeLog) advanceNativePrefix() {
	if l.invalidPrefixFrom > 0 && l.invalidPrefixFrom <= l.contiguousThrough.Sequence {
		l.contiguousThrough = nativePosition{}
	}
	for sequence := l.contiguousThrough.Sequence + 1; ; sequence++ {
		id, exists := l.sequences[sequence]
		if !exists || l.invalid[id] {
			return
		}
		record, exists := l.records[id]
		if !exists || record.record.Sequence != sequence {
			return
		}
		l.contiguousThrough = nativePosition{ID: id, Sequence: sequence}
	}
}

func (l *nativeLog) hasNativePrefixThrough(tail nativePosition) (bool, error) {
	if tail.ID == "" || tail.Sequence <= 0 {
		return false, fmt.Errorf("the Muse native prefix has no valid tail")
	}
	if l.invalidPrefixFrom > 0 && l.invalidPrefixFrom <= tail.Sequence {
		return false, fmt.Errorf("the Muse native prefix contains conflicting evidence")
	}
	if id, observed := l.sequences[tail.Sequence]; observed && id != tail.ID {
		return false, fmt.Errorf("the Muse native prefix has a different tail identity")
	}
	return l.contiguousThrough.Sequence >= tail.Sequence, nil
}

func (l *nativeLog) nativeRecordsBetween(after, through nativePosition) ([]storedRecord, bool, error) {
	complete, err := l.hasNativePrefixThrough(through)
	if err != nil || !complete {
		return nil, complete, err
	}
	if after.Sequence < 0 || after.Sequence > through.Sequence {
		return nil, false, fmt.Errorf("the Muse native prefix interval is invalid")
	}
	if after.Sequence > 0 && l.sequences[after.Sequence] != after.ID {
		return nil, false, fmt.Errorf("the Muse native prefix interval has a different start identity")
	}
	result := make([]storedRecord, 0, through.Sequence-after.Sequence)
	for sequence := after.Sequence + 1; sequence <= through.Sequence; sequence++ {
		result = append(result, l.records[l.sequences[sequence]])
	}
	return result, true, nil
}
