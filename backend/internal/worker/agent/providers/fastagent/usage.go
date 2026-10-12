package fastagent

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var fastagentStatusLinePattern = regexp.MustCompile(`^([0-9][0-9,]*) in, ([0-9][0-9,]*) out(?:, [0-9][0-9,]* tools)?(?: \(([0-9]+(?:\.[0-9]+)?)%\))?$`)

// broadcastFastagentStatusLine reads the metrics that Fast Agent adds to the
// outer ACP session/update metadata. It does not infer a context-window size.
func broadcastFastagentStatusLine(sink agent.ProviderServices, metadata map[string]json.RawMessage) {
	if sink == nil {
		return
	}
	metrics := metadata["openhands.dev/metrics"]
	if len(metrics) == 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(metadata["field_meta"], &fields) != nil {
			return
		}
		metrics = fields["openhands.dev/metrics"]
	}
	var native struct {
		StatusLine string `json:"status_line"`
	}
	if json.Unmarshal(metrics, &native) != nil {
		return
	}
	usage, ok := fastagentStatusLineUsage(native.StatusLine)
	if !ok {
		return
	}
	sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: usage})
}

func fastagentStatusLineUsage(line string) (map[string]any, bool) {
	parts := fastagentStatusLinePattern.FindStringSubmatch(line)
	if parts == nil {
		return nil, false
	}
	input, ok := fastagentGroupedCount(parts[1])
	if !ok {
		return nil, false
	}
	output, ok := fastagentGroupedCount(parts[2])
	if !ok {
		return nil, false
	}
	usage := providerkit.ContextUsageMap(providerkit.ContextTokenCounts{Input: input, Output: output})
	if parts[3] != "" {
		percent, err := strconv.ParseFloat(parts[3], 64)
		if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) {
			return nil, false
		}
		usage[contracts.ContextUsageFieldUsagePercent] = percent
	}
	return usage, true
}

func fastagentGroupedCount(value string) (int64, bool) {
	groups := strings.Split(value, ",")
	if len(groups) > 1 {
		if len(groups[0]) < 1 || len(groups[0]) > 3 {
			return 0, false
		}
		for _, group := range groups[1:] {
			if len(group) != 3 {
				return 0, false
			}
		}
	}
	for _, group := range groups {
		for _, digit := range group {
			if digit < '0' || digit > '9' {
				return 0, false
			}
		}
	}
	count, err := strconv.ParseInt(strings.Join(groups, ""), 10, 64)
	return count, err == nil
}
