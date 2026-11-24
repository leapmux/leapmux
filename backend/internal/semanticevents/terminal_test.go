package semanticevents

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestTerminalFactCopiesOnlySemanticFields(t *testing.T) {
	t.Parallel()
	status := &leapmuxv1.TerminalStatusChange{
		Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
		StartupError: "could not start",
	}
	status.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
	fact := TerminalFact(&leapmuxv1.TerminalEvent{
		TerminalId: "private terminal ID",
		Event:      &leapmuxv1.TerminalEvent_StatusChange{StatusChange: status},
	})
	require.NotNil(t, fact)
	assert.True(t, proto.Equal(&leapmuxv1.SemanticTerminalStatus{
		Status:       leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED,
		StartupError: "could not start",
	}, fact.GetTerminalStatus()))
	assert.Empty(t, fact.ProtoReflect().GetUnknown())
	status.StartupError = "changed after capture"
	assert.Equal(t, "could not start", fact.GetTerminalStatus().GetStartupError())

	closed := func(code *int32) *leapmuxv1.TerminalClosed {
		return TerminalFact(&leapmuxv1.TerminalEvent{
			Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{ExitCode: code}},
		}).GetTerminalExit()
	}
	assert.Nil(t, closed(nil).ExitCode)
	var success int32
	recorded := closed(&success)
	success = 9
	require.NotNil(t, recorded.ExitCode)
	assert.Zero(t, *recorded.ExitCode)
	assert.NotNil(t, TerminalFact(&leapmuxv1.TerminalEvent{
		Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{}},
	}).GetTerminalTitle())
}

func TestTerminalFactIgnoresScreenDataAndMissingEvents(t *testing.T) {
	t.Parallel()
	for _, event := range []*leapmuxv1.TerminalEvent{
		nil,
		{},
		{Event: &leapmuxv1.TerminalEvent_StatusChange{StatusChange: &leapmuxv1.TerminalStatusChange{}}},
		{Event: &leapmuxv1.TerminalEvent_Closed{}},
		{Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("screen bytes")}}},
		{Event: &leapmuxv1.TerminalEvent_ScreenGeometry{ScreenGeometry: &leapmuxv1.TerminalScreenGeometry{Cols: 240, Rows: 25}}},
	} {
		assert.Nil(t, TerminalFact(event))
	}
}
