package pi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piFreshImplementationFixture() (*Agent, *agenttest.ControlSink, *bytes.Buffer) {
	output := &bytes.Buffer{}
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.NopStdin(output))
	return a, sink, output
}

const piFreshSettingsDialog = `{"type":"extension_ui_request","id":"fresh-settings","method":"select","title":"Fresh implementation settings","options":["Start fresh implementation","Model","Thinking level"]}`

// publishedRequestIDs lists the dialog ids that reached the UI. The plan-ready
// dialog is published in production too -- the approval banner renders it -- so
// tests isolate the settings dialog by id instead of asserting on every record.
func publishedRequestIDs(sink *agenttest.ControlSink) []string {
	ids := make([]string, 0)
	for _, record := range sink.PublishedControls() {
		ids = append(ids, record.RequestID)
	}
	return ids
}

// lastStdinLine returns the final JSONL line written to Pi, skipping the
// forwarded approval that precedes the auto-answer under test.
func lastStdinLine(t *testing.T, output *bytes.Buffer) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(output.String(), "\n"), "\n")
	require.NotEmpty(t, lines)
	return lines[len(lines)-1]
}

func approvePiFreshImplementation(t *testing.T, a *Agent) {
	t.Helper()
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"plan-ready","method":"select","title":"Proposed plan ready. What next?","options":["Implement here","Start fresh and implement","Export plan…","Stay in Plan mode"]}`))
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"plan-ready","value":"Start fresh and implement"}`)))
}

func TestPiFreshSettingsDialogAnsweredAfterFreshApproval(t *testing.T) {
	t.Parallel()
	a, sink, output := piFreshImplementationFixture()
	approvePiFreshImplementation(t, a)

	a.handlePiExtensionUIRequest([]byte(piFreshSettingsDialog))

	assert.Equal(t, []string{"plan-ready"}, publishedRequestIDs(sink), "the settings dialog must not reach the UI")
	assert.JSONEq(t,
		`{"type":"extension_ui_response","id":"fresh-settings","value":"Start fresh implementation"}`,
		lastStdinLine(t, output))
	a.Mu.Lock()
	pending := a.freshImplementationPending
	a.Mu.Unlock()
	assert.False(t, pending, "the auto-answer consumes the mark")
}

func TestPiFreshSettingsDialogPublishedWithoutFreshApproval(t *testing.T) {
	t.Parallel()
	a, sink, output := piFreshImplementationFixture()

	a.handlePiExtensionUIRequest([]byte(piFreshSettingsDialog))

	assert.Equal(t, []string{"fresh-settings"}, publishedRequestIDs(sink))
	assert.Empty(t, output.String(), "no answer is sent for a dialog the user never approved")
}

func TestPiFreshSettingsDialogMarkSurvivesUnrelatedDialog(t *testing.T) {
	t.Parallel()
	a, sink, output := piFreshImplementationFixture()
	approvePiFreshImplementation(t, a)

	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"unrelated","method":"confirm","title":"Fresh implementation settings","message":"Unrelated"}`))
	assert.Equal(t, []string{"plan-ready", "unrelated"}, publishedRequestIDs(sink), "a confirm dialog is never the settings screen")

	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"retitled","method":"select","title":"Fresh implementation settings\n\nExtra lines","options":["Start fresh implementation","Model","Thinking level"]}`))
	assert.Equal(t, []string{"plan-ready", "unrelated"}, publishedRequestIDs(sink), "a retitled settings dialog is still answered")
	assert.JSONEq(t,
		`{"type":"extension_ui_response","id":"retitled","value":"Start fresh implementation"}`,
		lastStdinLine(t, output))
}

func TestPiFreshSettingsDialogMarkSurvivesForeignOptions(t *testing.T) {
	t.Parallel()
	a, sink, _ := piFreshImplementationFixture()
	approvePiFreshImplementation(t, a)

	// A same-titled dialog from another extension would not offer the plan
	// extension's start action; it must reach the user instead of being
	// answered with a value it does not carry.
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"foreign","method":"select","title":"Fresh implementation settings","options":["Model","Thinking level"]}`))
	assert.Equal(t, []string{"plan-ready", "foreign"}, publishedRequestIDs(sink))
	a.Mu.Lock()
	pending := a.freshImplementationPending
	a.Mu.Unlock()
	assert.True(t, pending, "the mark waits for the real settings dialog")
}

func TestPiFreshSettingsDialogAnswerFailureFallsBackToPublishing(t *testing.T) {
	t.Parallel()
	a, sink, _ := piFreshImplementationFixture()
	approvePiFreshImplementation(t, a)
	a.SetStdinForTest(nil)

	// A nil stdin writer fails the send; the dialog must then be published so
	// the user can still answer it.
	a.handlePiExtensionUIRequest([]byte(piFreshSettingsDialog))
	assert.Equal(t, []string{"plan-ready", "fresh-settings"}, publishedRequestIDs(sink))
}

func TestPiPlanFreshApprovalIgnoredWhenValueDiffers(t *testing.T) {
	t.Parallel()
	a, sink, _ := piFreshImplementationFixture()
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"plan-ready","method":"select","title":"Proposed plan ready. What next?","options":["Implement here","Start fresh and implement"]}`))
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"plan-ready","value":"`+contracts.PiPlanActionImplementHere+`"}`)))

	a.Mu.Lock()
	pending := a.freshImplementationPending
	a.Mu.Unlock()
	assert.False(t, pending, "an in-place approval must not arm the fresh auto-answer")
	assert.Equal(t, []string{"plan-ready"}, publishedRequestIDs(sink))
}

func TestPiCancelledPlanResponseDoesNotArmFreshAutoAnswer(t *testing.T) {
	t.Parallel()
	a, _, _ := piFreshImplementationFixture()
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"plan-ready","method":"select","title":"Proposed plan ready. What next?","options":["Implement here","Start fresh and implement"]}`))
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"plan-ready","cancelled":true}`)))

	a.Mu.Lock()
	pending := a.freshImplementationPending
	a.Mu.Unlock()
	assert.False(t, pending, "a cancelled dialog never chose the fresh implementation")
}

func TestPiClearContextDropsFreshImplementationMark(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	defer rig.cleanup()
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		switch req.Type {
		case CommandNewSession:
			return json.RawMessage(`{"cancelled":false}`), true, ""
		case CommandGetState:
			return json.RawMessage(`{"sessionId":"fresh-2","sessionFile":"/tmp/pi-fresh-2.jsonl"}`), true, ""
		}
		return json.RawMessage(`{}`), true, ""
	})

	approvePiFreshImplementation(t, rig.agent)
	_, err := rig.agent.ClearContext()
	require.NoError(t, err)

	rig.agent.Mu.Lock()
	pending := rig.agent.freshImplementationPending
	rig.agent.Mu.Unlock()
	assert.False(t, pending, "the mark dies with the session it belonged to")

	// The plan menu the approval answered also died with the session, so a
	// settings dialog that still slips through must reach the user.
	rig.agent.handlePiExtensionUIRequest([]byte(piFreshSettingsDialog))
	assert.Equal(t, []string{"plan-ready", "fresh-settings"}, publishedRequestIDs(sink))
}

func TestPiDialogTitleFirstLine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		title string
		want  string
	}{
		{"Fresh implementation settings", "Fresh implementation settings"},
		{"Fresh implementation settings\n\nStart fresh transfers only the approved plan.", "Fresh implementation settings"},
		{"Fresh implementation settings\r\n\r\nCarriage returns too", "Fresh implementation settings"},
		{"", ""},
		{"\nstarts on the second line", ""},
	} {
		assert.Equal(t, tc.want, piDialogTitleFirstLine(tc.title), "title %q", tc.title)
	}
}
