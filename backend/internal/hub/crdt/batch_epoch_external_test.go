package crdt_test

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// batchesAtEpoch constructs client submission fixtures under the specified epoch.
func batchesAtEpoch(epoch int64, batches []*leapmuxv1.OpBatch) []*leapmuxv1.OpBatch {
	for _, batch := range batches {
		if batch != nil {
			batch.Epoch = epoch
		}
	}
	return batches
}
