package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestPlanApprovalSettingsPersistOutsideTheNativeResponse(t *testing.T) {
	for _, settings := range []*leapmuxv1.PlanApprovalSettings{
		{}, {PermissionMode: "default"},
	} {
		t.Run(settings.PermissionMode, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			}))
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim",
				Payload: []byte(`{"type":"control_request","request_id":"plan","request":{"tool_name":"ExitPlanMode","input":{"plan":"# Keep this plan"}}}`),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			response := []byte(" {\"type\":\"control_response\",\"extra\":9007199254740993,\"response\":{\"subtype\":\"success\",\"request_id\":\"plan\",\"response\":{\"behavior\":\"allow\"}}} \n")
			var sent [][]byte
			svc.sendControlResponseFn = func(_ string, bytes []byte) error {
				sent = append(sent, append([]byte(nil), bytes...))
				return nil
			}
			request := &leapmuxv1.SendControlResponseRequest{AgentId: "agent-1", RequestId: "plan", ClaimToken: "claim", Content: response, PlanApproval: settings}
			require.NoError(t, svc.processControlResponse(row, request))
			expectedNative := response
			if settings.PermissionMode != "" {
				expectedNative = bytes.Replace(response, []byte(`"behavior":"allow"`), []byte(`"behavior":"allow","updatedPermissions":[{"destination":"session","mode":"default","type":"setMode"}]`), 1)
			}
			require.Equal(t, [][]byte{expectedNative}, sent)
			answer, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim"})
			require.NoError(t, err)
			require.Equal(t, response, answer.ResponseContent)
			require.Equal(t, expectedNative, answer.ResolvedContent)
			var recorded leapmuxv1.PlanApprovalSettings
			require.NoError(t, protojson.Unmarshal(answer.PlanApprovalSettings, &recorded))
			require.True(t, proto.Equal(settings, &recorded))
			require.JSONEq(t, `{"permissionMode":"`+settings.PermissionMode+`","clearContext":false}`, string(answer.PlanApprovalSettings))
			request.PlanApproval = &leapmuxv1.PlanApprovalSettings{PermissionMode: "bypassPermissions", ClearContext: true}
			require.NoError(t, svc.processControlResponse(row, request))
			require.Len(t, sent, 1, "a completed response cannot resend or replace its saved settings")
			retained, err := svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{AgentID: "agent-1", RequestID: "plan", ClaimToken: "claim"})
			require.NoError(t, err)
			require.Equal(t, answer.PlanApprovalSettings, retained.PlanApprovalSettings)
		})
	}
}

func TestPlanSettingsRequireAMatchingApprovalBeforeReservation(t *testing.T) {
	for _, tc := range []struct {
		tool, behavior, mode, expectedError string
	}{
		{"Bash", "allow", "default", "matching plan request"},
		{"ExitPlanMode", "deny", "default", "matching plan request"},
		{"ExitPlanMode", "allow", "\xff", "encode plan approval settings"},
	} {
		t.Run(tc.tool+"-"+tc.behavior+"-"+tc.expectedError, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
				ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			}))
			createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
				Payload: []byte(fmt.Sprintf(`{"request":{"tool_name":%q}}`, tc.tool)),
			})
			row, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)
			writes := 0
			svc.sendControlResponseFn = func(string, []byte) error { writes++; return nil }
			err = svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
				ClaimToken: "claim", PlanApproval: &leapmuxv1.PlanApprovalSettings{PermissionMode: tc.mode},
				Content: []byte(fmt.Sprintf(`{"response":{"request_id":"request","response":{"behavior":%q}}}`, tc.behavior)),
			})
			require.ErrorContains(t, err, tc.expectedError)
			require.Zero(t, writes)
			_, err = svc.Queries.GetControlResponseAnswer(t.Context(), db.GetControlResponseAnswerParams{
				AgentID: "agent-1", RequestID: "request", ClaimToken: "claim",
			})
			require.ErrorIs(t, err, sql.ErrNoRows)
		})
	}
}

func TestSavedPlanSettingsStaySeparateDuringRecording(t *testing.T) {
	response := []byte(` {"type":"control_response","response":{"subtype":"success","request_id":"plan","response":{"behavior":"allow"}},"large":9007199254740993} `)
	request := []byte(` {"request":{"tool_name":"ExitPlanMode","input":{"plan":"# Plan"}},"extra":false} `)
	for _, settings := range []string{`{"permissionMode":"","clearContext":false}`, `{"permissionMode":"default","clearContext":true}`} {
		plan, err := controlResponsePlanFromAnswer(db.ControlResponseAnswer{
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			RequestID:     "plan", RequestPayload: request, ResponseContent: response, ResolvedContent: response,
			PlanApprovalSettings: []byte(settings),
		})
		require.NoError(t, err)
		content, err := controlResponseMessageContent(plan)
		require.NoError(t, err)
		require.Equal(t, response, content.Original)
		require.Equal(t, request, content.Supplemental)
		var metadata map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(content.Metadata, &metadata))
		require.JSONEq(t, settings, string(metadata["plan_approval_settings"]))
	}
	_, err := controlResponsePlanFromAnswer(db.ControlResponseAnswer{PlanApprovalSettings: []byte(`{"clearContext":`)})
	require.ErrorContains(t, err, "read saved plan approval settings")
}

func TestRecordingRejectsNewPlanSettings(t *testing.T) {
	svc, _, _ := setupTestService(t)
	_, err := svc.respondToControlRequest(db.Agent{ID: "agent-1"}, &leapmuxv1.SendControlResponseRequest{
		RequestId: "plan", RecordOnly: true, PlanApproval: &leapmuxv1.PlanApprovalSettings{},
	})
	require.ErrorContains(t, err, "a recording request cannot contain a new response or plan settings")
}

func TestClaudePlanApprovalIncludesTheNativePermissionUpdate(t *testing.T) {
	svc, _, _ := setupTestService(t)
	row := createPlanSessionTestAgent(t, svc, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "ExitPlanMode")
	var delivered []byte
	svc.sendControlResponseFn = func(_ string, content []byte) error {
		delivered = append([]byte(nil), content...)
		return nil
	}
	require.NoError(t, svc.processControlResponse(row, &leapmuxv1.SendControlResponseRequest{
		ClaimToken: "claim", Content: []byte(`{"response":{"request_id":"plan","response":{"behavior":"allow","updatedInput":{}}}}`),
		PlanApproval: &leapmuxv1.PlanApprovalSettings{PermissionMode: "bypassPermissions"},
	}))
	var native struct {
		Response struct {
			Response struct {
				UpdatedPermissions []map[string]string `json:"updatedPermissions"`
			} `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(delivered, &native))
	require.Equal(t, []map[string]string{{"type": "setMode", "mode": "bypassPermissions", "destination": "session"}}, native.Response.Response.UpdatedPermissions)
}

// The transcript row repeats the bytes the claim row holds; it does not encode
// the message a second time. One value therefore has one encoding, and the
// stored form cannot be reshaped by a later change to the marshal options --
// which is what a settings blob written without EmitDefaultValues would meet.
func TestSavedPlanSettingsReachTheTranscriptAsStored(t *testing.T) {
	response := []byte(`{"response":{"subtype":"success","request_id":"plan","response":{"behavior":"allow"}}}`)
	stored := []byte(`{"permissionMode":"default"}`)
	plan, err := controlResponsePlanFromAnswer(db.ControlResponseAnswer{
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		RequestID:     "plan", RequestPayload: []byte(`{"request":{"tool_name":"ExitPlanMode"}}`),
		ResponseContent: response, ResolvedContent: response,
		PlanApprovalSettings: stored,
	})
	require.NoError(t, err)
	require.Equal(t, stored, plan.settingsJSON)

	content, err := controlResponseMessageContent(plan)
	require.NoError(t, err)
	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content.Metadata, &metadata))
	require.Equal(t, string(stored), string(metadata["plan_approval_settings"]),
		"a second marshal would add the default clearContext the stored bytes omit")
}
