package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// piMessageEndBenchPayload is a representative ~1.5 KB assistant
// message_end envelope. Sized to mirror a realistic Pi turn: short
// assistant text plus the usage block. Larger payloads only widen the
// gap between the old double-decode path and the single-decode one.
var piMessageEndBenchPayload = []byte(`{
  "type":"message_end",
  "message":{
    "role":"assistant",
    "content":[
      {"type":"thinking","thinking":"Let me think about this..."},
      {"type":"text","text":"Here is a multi-line answer.\nWith several lines.\nUseful for benchmarking."}
    ],
    "usage":{
      "input":4231,
      "output":182,
      "cacheRead":1024,
      "cacheWrite":256,
      "totalTokens":5693,
      "cost":{
        "input":0.00042,
        "output":0.00018,
        "cacheRead":0.00001,
        "cacheWrite":0.00002,
        "total":0.00063
      }
    }
  }
}`)

// BenchmarkAugmentPiMessageEnd records the per-message_end cost of the
// single-decode path. The previous implementation did two full
// json.Unmarshals of the same envelope; the new path does one decode,
// extracts the typed usage from the decoded map, mutates fields in
// place, and marshals once. Run with -benchmem to see allocs/op.
func BenchmarkAugmentPiMessageEnd(b *testing.B) {
	a := newPiAgentWithSink(&recordingControlSink{})
	a.model = "gpt-5"
	a.availableModels = []*leapmuxv1.AvailableModel{{Id: "gpt-5", ContextWindow: 200000}}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = a.augmentPiMessageEnd(piMessageEndBenchPayload)
	}
}

// piAgentEndBenchPayload mirrors the agent_end persist path. agent_end
// has no `message.usage` so the augment helper exercises only the
// snapshot-injection branch.
var piAgentEndBenchPayload = []byte(`{
  "type":"agent_end",
  "messages":[],
  "elapsed":1234
}`)

// BenchmarkPersistPiAgentEnd benchmarks the agent_end augment path
// (single Unmarshal + Marshal, identical between old and new code).
// Useful as a baseline alongside BenchmarkAugmentPiMessageEnd to
// confirm the gain on message_end is a real reduction, not measurement
// noise.
func BenchmarkPersistPiAgentEnd(b *testing.B) {
	snap := piUsageSnapshot{
		HasTotalCost: true,
		TotalCostUsd: 0.42,
		ContextUsage: map[string]any{
			"input_tokens":                int64(4231),
			"cache_creation_input_tokens": int64(256),
			"cache_read_input_tokens":     int64(1024),
			"output_tokens":               int64(182),
			"context_window":              int64(200000),
		},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = piAugmentRawWithSnapshot(piAgentEndBenchPayload, snap)
	}
}

// BenchmarkRecordPiAssistantUsage isolates the per-call clone cost.
// After Fix 3 (which dropped the third clone in sessionInfo), this
// should show two maps.Clone calls per record: the defensive copy of
// the caller's map and the snapshot's isolation copy.
func BenchmarkRecordPiAssistantUsage(b *testing.B) {
	a := newPiAgentWithSink(&recordingControlSink{})
	usage := piAssistantUsage{Input: 100, Output: 10, CacheRead: 20, CacheWrite: 5}
	usage.Cost.Total = 0.001
	context := map[string]any{
		"input_tokens":                int64(100),
		"cache_creation_input_tokens": int64(5),
		"cache_read_input_tokens":     int64(20),
		"output_tokens":               int64(10),
		"context_window":              int64(200000),
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = a.recordPiAssistantUsage(usage, context)
	}
}
