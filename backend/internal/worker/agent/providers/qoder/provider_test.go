package qoder

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestQoderClassify(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want agent.NotificationClassification
	}{
		{`{"type":"system","subtype":"compact_boundary"}`, agent.CompactionBoundaryNotification()},
		{`{"type":"system","subtype":"api_retry"}`, agent.APIRetryNotification()},
		{`{"type":"system","subtype":"model_queue_status"}`, agent.ProviderScopedNotification("qoder:model-queue")},
		{`{"type":"system","subtype":"status","status":"compacting"}`, agent.NotificationClassification{}},
		{`{"type":"assistant","subtype":"api_retry"}`, agent.NotificationClassification{}},
		{`{"type":"system","subtype":"future"}`, agent.NotificationClassification{}},
		{`{`, agent.NotificationClassification{}},
	} {
		assert.Equal(t, tc.want, (qoderProvider{}).Classify(json.RawMessage(tc.raw)), tc.raw)
	}
}
