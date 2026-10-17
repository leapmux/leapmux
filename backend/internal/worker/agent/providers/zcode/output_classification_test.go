package zcode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeSessionUpdateContentTypeUsesTheDecodedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want zcodeSessionUpdateKind
	}{
		{`""`, zcodeSessionUpdateModelResponse},
		{`"escaped\ntext"`, zcodeSessionUpdateModelResponse},
		{`  "spaced"  `, zcodeSessionUpdateModelResponse},
		{`null`, zcodeSessionUpdateDrop},
		{`false`, zcodeSessionUpdateDrop},
		{`0`, zcodeSessionUpdateDrop},
		{`[]`, zcodeSessionUpdateDrop},
		{`{}`, zcodeSessionUpdateDrop},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var payload zcodeSessionUpdatedPayload
			require.NoError(t, json.Unmarshal([]byte(`{"content":`+tc.raw+`,"stopReason":"stop"}`), &payload))
			assert.Equal(t, tc.want, payload.kind())
		})
	}
}

func BenchmarkZCodeSessionUpdateKindLargeContent(b *testing.B) {
	raw, err := json.Marshal(map[string]string{"content": strings.Repeat("x", 256<<10), "stopReason": "stop"})
	if err != nil {
		b.Fatal(err)
	}
	var payload zcodeSessionUpdatedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if payload.kind() != zcodeSessionUpdateModelResponse {
			b.Fatal("the response classification changed")
		}
	}
}

func TestZCodeSessionUpdateClassificationDoesNotCopyTheReply(t *testing.T) {
	result := testing.Benchmark(BenchmarkZCodeSessionUpdateKindLargeContent)
	// Classification needs only the JSON type, not another copy of the 256 KiB reply.
	require.Less(t, result.AllocedBytesPerOp(), int64(4096))
}
