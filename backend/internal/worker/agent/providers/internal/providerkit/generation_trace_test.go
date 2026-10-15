package providerkit

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationBufferReceipt(t *testing.T) {
	const childEnv = "LEAPMUX_TEST_GENERATION_RECEIPT_CHILD"
	if os.Getenv(childEnv) == "1" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
		var buffer GenerationBuffer
		buffer.Append("", agent.AssembledMessageKindText, "ignored", JoinVerbatim)
		buffer.Append("scope", agent.AssembledMessageKindText, "", JoinVerbatim)
		buffer.Append("scope", agent.AssembledMessageKindText, "a", JoinVerbatim)
		buffer.Append("scope", agent.AssembledMessageKindText, "bc", JoinVerbatim)
		return
	}
	t.Parallel()
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGenerationBufferReceipt$")
			command.Env = envutil.PinEnv(os.Environ(), childEnv+"=1", contracts.GenerationTraceEnv+"="+enabled)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			var receipts []map[string]any
			for line := range strings.SplitSeq(string(output), "\n") {
				if !strings.HasPrefix(line, "{") {
					continue
				}
				var record map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &record))
				if record["marker"] == contracts.GenerationTraceMarker {
					receipts = append(receipts, record)
				}
			}
			if enabled == "0" {
				assert.Empty(t, receipts)
				return
			}
			require.Len(t, receipts, 2)
			assert.Equal(t, "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb", receipts[0][contracts.GenerationTraceFieldSHA256])
			assert.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", receipts[1][contracts.GenerationTraceFieldSHA256])
			assert.Equal(t, float64(3), receipts[1][contracts.GenerationTraceFieldBytes])
			for _, record := range receipts {
				assert.Equal(t, "scope", record[contracts.GenerationTraceFieldScope])
				assert.Equal(t, "text", record[contracts.GenerationTraceFieldKind])
				assert.Equal(t, "generation buffer receipt", record["msg"])
				assert.NotContains(t, record, "text")
			}
		})
	}
}
