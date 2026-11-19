package kimi

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationToolSpanConformance(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(testutil.RepoPath(t, "testdata", "kimi_conversation_span_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct{ Name, Session, Agent, Turn, Item, Span string }
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			view := kimiConversationRecordView{record: contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindItem, SessionID: test.Session, AgentID: test.Agent, TurnID: test.Turn, Role: contracts.KimiConversationRecordRoleRequest}, item: &kimiConversationItem{ItemID: test.Item}}
			assert.Equal(t, test.Span, view.key())
		})
	}
}
