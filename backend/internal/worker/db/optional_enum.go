package db

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

type optionalStorageEnum interface {
	leapmuxv1.MarkType | leapmuxv1.AssembledMessageKind | leapmuxv1.MessageCompletion |
		leapmuxv1.AgentInputQueuePauseReason | leapmuxv1.AgentInputQueuePauseOwner
}

// OptionalStorageEnum maps a wire absence to SQL NULL.
// The storage boundary validates every nonzero ordinal.
func OptionalStorageEnum[T optionalStorageEnum](value T) *T {
	if value == 0 {
		return nil
	}
	return &value
}

// StorageEnumValue maps SQL NULL to the wire's unspecified default.
func StorageEnumValue[T optionalStorageEnum](value *T) T {
	if value == nil {
		return 0
	}
	return *value
}
