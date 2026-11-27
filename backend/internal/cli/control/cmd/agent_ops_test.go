package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

type agentOpsDispatcher struct {
	mu                    sync.Mutex
	calls                 []string
	agents                []*leapmuxv1.AgentInfo
	messages              []*leapmuxv1.AgentChatMessage
	sessionInfoJSON       string
	sessionInfoCode       codes.Code
	sessionInfoError      string
	approveReviewCode     codes.Code
	approveReviewError    string
	pages                 map[int64]*leapmuxv1.ListAgentMessagesResponse
	pageErrAtCursor       map[int64]codes.Code
	retryRequests         []*leapmuxv1.RetryAgentTurnRequest
	approveReviewRequests []*leapmuxv1.ApproveAutoReviewDenialRequest
	startupRequests       []*leapmuxv1.RespondAgentStartupPromptRequest
	getMessageRequests    []*leapmuxv1.GetAgentMessageRequest
	getSpanRequests       []*leapmuxv1.GetAgentSpanMessagesRequest
	listMessageRequests   []*leapmuxv1.ListAgentMessagesRequest
}

func (d *agentOpsDispatcher) DispatchWith(_ context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	d.mu.Lock()
	method := req.GetMethod()
	d.calls = append(d.calls, method)
	d.mu.Unlock()

	switch method {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{Agents: d.agents})
	case leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure:
		var in leapmuxv1.RespondAgentStartupPromptRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.startupRequests = append(d.startupRequests, &in)
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.RespondAgentStartupPromptResponse{})
	case leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure:
		if d.sessionInfoCode != codes.OK {
			_ = rw.SendError(int32(d.sessionInfoCode), d.sessionInfoError)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.GetAgentSessionInfoResponse{SessionInfoJson: d.sessionInfoJSON})
	case leapmuxv1connect.WorkerAgentServiceRetryAgentTurnProcedure:
		var in leapmuxv1.RetryAgentTurnRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.retryRequests = append(d.retryRequests, &in)
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.RetryAgentTurnResponse{})
	case leapmuxv1connect.WorkerAgentServiceApproveAutoReviewDenialProcedure:
		var in leapmuxv1.ApproveAutoReviewDenialRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.approveReviewRequests = append(d.approveReviewRequests, &in)
		code, msg := d.approveReviewCode, d.approveReviewError
		d.mu.Unlock()
		if code != codes.OK {
			_ = rw.SendError(int32(code), msg)
			return
		}
		writeProtoResponse(rw, &leapmuxv1.ApproveAutoReviewDenialResponse{})
	case leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure:
		var in leapmuxv1.GetAgentMessageRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.getMessageRequests = append(d.getMessageRequests, &in)
		d.mu.Unlock()
		var msg *leapmuxv1.AgentChatMessage
		if len(d.messages) > 0 {
			msg = d.messages[0]
		}
		writeProtoResponse(rw, &leapmuxv1.GetAgentMessageResponse{Message: msg})
	case leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure:
		var in leapmuxv1.GetAgentSpanMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.getSpanRequests = append(d.getSpanRequests, &in)
		d.mu.Unlock()
		writeProtoResponse(rw, &leapmuxv1.GetAgentSpanMessagesResponse{Messages: d.messages})
	case leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure:
		var in leapmuxv1.ListAgentMessagesRequest
		_ = proto.Unmarshal(req.GetPayload(), &in)
		d.mu.Lock()
		d.listMessageRequests = append(d.listMessageRequests, &in)
		d.mu.Unlock()
		cursor := in.GetCursorSeq()
		if code, fail := d.pageErrAtCursor[cursor]; fail {
			_ = rw.SendError(int32(code), "worker page failed")
			return
		}
		resp := d.pages[cursor]
		if resp == nil {
			resp = &leapmuxv1.ListAgentMessagesResponse{}
		}
		writeProtoResponse(rw, resp)
	default:
		_ = rw.SendError(int32(codes.Unimplemented), "unexpected method: "+method)
	}
}

func (d *agentOpsDispatcher) recordedCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func TestRunAgentRetryTurn_PropagatesSessionInfoErrors(t *testing.T) {
	agID := fullTestID("ag-retry")

	t.Run("RPC error is returned instead of not_found", func(t *testing.T) {
		w := &agentOpsDispatcher{
			sessionInfoCode:  codes.Unavailable,
			sessionInfoError: "session store offline",
		}
		startAgentWorker(t, agID, w)

		f := runControl(t, "agent", "retry-turn", agID).requireFailure(t, cli.CodeUnavailable)
		assert.Contains(t, f.Message, "session store offline")
		assert.Empty(t, w.retryRequests)
	})

	t.Run("malformed session_info_json returns internal error", func(t *testing.T) {
		w := &agentOpsDispatcher{
			sessionInfoJSON: "{not-valid-json",
		}
		startAgentWorker(t, agID, w)

		f := runControl(t, "agent", "retry-turn", agID).requireFailure(t, cli.CodeInternal)
		assert.Contains(t, f.Message, "parse agent session info")
		assert.Empty(t, w.retryRequests)
	})

	t.Run("valid JSON without retry offer returns not_found", func(t *testing.T) {
		w := &agentOpsDispatcher{
			sessionInfoJSON: `{"total_cost_usd": 0.1}`,
		}
		startAgentWorker(t, agID, w)

		f := runControl(t, "agent", "retry-turn", agID).requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no active turn retry offer")
	})

	t.Run("explicit turn-id and model skip GetAgentSessionInfo", func(t *testing.T) {
		w := &agentOpsDispatcher{
			sessionInfoCode:  codes.Unavailable,
			sessionInfoError: "should not be called",
		}
		startAgentWorker(t, agID, w)

		data := runControl(t, "agent", "retry-turn", agID, "--turn-id", " turn-7 ", "--model", " fast-model ").Data(t)
		assert.Equal(t, agID, data["id"])
		assert.Equal(t, "turn-7", data["turn_id"])
		assert.Equal(t, "fast-model", data["model"])
		assert.NotContains(t, w.recordedCalls(), leapmuxv1connect.WorkerAgentServiceGetAgentSessionInfoProcedure)
		require.Len(t, w.retryRequests, 1)
		assert.Equal(t, agID, w.retryRequests[0].GetAgentId())
		assert.Equal(t, "turn-7", w.retryRequests[0].GetTurnId())
		assert.Equal(t, "fast-model", w.retryRequests[0].GetModel())
	})

	t.Run("partial flag overrides keep missing field from session info offer", func(t *testing.T) {
		w := &agentOpsDispatcher{
			sessionInfoJSON: `{"turn_retry_offer":{"turn_id":" turn-11 ","model":" offer-model "}}`,
		}
		startAgentWorker(t, agID, w)

		data := runControl(t, "agent", "retry-turn", agID, "--model", "custom-fast").Data(t)
		assert.Equal(t, "turn-11", data["turn_id"])
		assert.Equal(t, "custom-fast", data["model"])
		require.Len(t, w.retryRequests, 1)
		assert.Equal(t, "turn-11", w.retryRequests[0].GetTurnId())
		assert.Equal(t, "custom-fast", w.retryRequests[0].GetModel())
	})
}

func TestRunAgentApproveReview_RejectsBlankRequiredFlagsBeforeSession(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	agID := fullTestID("ag-rev")

	for name, args := range map[string][]string{
		"empty message-id":      {"agent", "approve-review", agID, "--message-id", "", "--review-id", "rev-1"},
		"whitespace message-id": {"agent", "approve-review", agID, "--message-id", "  \t ", "--review-id", "rev-1"},
		"empty review-id":       {"agent", "approve-review", agID, "--message-id", "msg-1", "--review-id", ""},
		"whitespace review-id":  {"agent", "approve-review", agID, "--message-id", "msg-1", "--review-id", "  \n "},
	} {
		t.Run(name, func(t *testing.T) {
			f := runControl(t, append(args, "--hub", "https://stub.invalid")...).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "is empty")
		})
	}
}

func TestRunAgentApproveReview_SendsTrimmedIDsAndPropagatesErrors(t *testing.T) {
	agID := fullTestID("ag-rev-ok")

	t.Run("happy path trims flags and returns review receipt", func(t *testing.T) {
		w := &agentOpsDispatcher{}
		startAgentWorker(t, agID, w)

		data := runControl(t, "agent", "approve-review", agID, "--message-id", " msg-7 ", "--review-id", " rev-3 ").Data(t)
		assert.Equal(t, agID, data["id"])
		assert.Equal(t, "msg-7", data["message_id"])
		assert.Equal(t, "rev-3", data["review_id"])
		require.Len(t, w.approveReviewRequests, 1)
		assert.Equal(t, agID, w.approveReviewRequests[0].GetAgentId())
		assert.Equal(t, "msg-7", w.approveReviewRequests[0].GetMessageId())
		assert.Equal(t, "rev-3", w.approveReviewRequests[0].GetReviewId())
	})

	t.Run("worker RPC error propagates its code", func(t *testing.T) {
		w := &agentOpsDispatcher{
			approveReviewCode:  codes.NotFound,
			approveReviewError: "review rev-9 not found",
		}
		startAgentWorker(t, agID, w)

		f := runControl(t, "agent", "approve-review", agID, "--message-id", "msg-7", "--review-id", "rev-9").
			requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "review rev-9 not found")
	})
}

func TestRunAgentExport_RejectsEmptyStyle(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	agID := fullTestID("ag-exp-style")

	for _, style := range []string{"", "   "} {
		f := runControl(t, "agent", "export", agID, "--style", style, "--hub", "https://stub.invalid").
			requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "--style must be one of markdown, text, json")
	}
}

func TestRunAgentExport_StreamsDeduplicatesAndWritesAtomically(t *testing.T) {
	agID := fullTestID("ag-exp")
	reply1, reply2, reply3 := "First reply", "Second reply", "Third reply"
	makePages := func() map[int64]*leapmuxv1.ListAgentMessagesResponse {
		return map[int64]*leapmuxv1.ListAgentMessagesResponse{
			0: {
				HasMore: true,
				Messages: []*leapmuxv1.AgentChatMessage{
					{Id: "m-1", Seq: 1, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, ReplyText: &reply1},
					{Id: "m-2", Seq: 2, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply2},
				},
			},
			// Page 2 repeats seq 2 (overlap) before seq 3 and claims HasMore=true.
			2: {
				HasMore: true,
				Messages: []*leapmuxv1.AgentChatMessage{
					{Id: "m-2", Seq: 2, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply2},
					{Id: "m-3", Seq: 3, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply3},
				},
			},
			// Page 3 stalls by returning seq 3 again; export must terminate cleanly.
			3: {
				HasMore: true,
				Messages: []*leapmuxv1.AgentChatMessage{
					{Id: "m-3", Seq: 3, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ReplyText: &reply3},
				},
			},
		}
	}

	t.Run("json style to output file deduplicates and matches schema", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{pages: makePages()})
		outPath := filepath.Join(t.TempDir(), "transcript.json")

		data := runControl(t, "agent", "export", agID, "--style", "json", "--output", outPath).Data(t)
		assert.Equal(t, agID, data["agent_id"])
		assert.Equal(t, float64(3), data["messages"])
		assert.Equal(t, outPath, data["output"])

		raw, err := os.ReadFile(outPath)
		require.NoError(t, err)
		var parsed agentMessageList
		require.NoError(t, json.Unmarshal(raw, &parsed))
		require.Len(t, parsed.Messages, 3)
		assert.Equal(t, int64(1), parsed.Messages[0].Seq)
		assert.Equal(t, int64(2), parsed.Messages[1].Seq)
		assert.Equal(t, int64(3), parsed.Messages[2].Seq)
	})

	t.Run("empty json style writes empty messages array", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{pages: map[int64]*leapmuxv1.ListAgentMessagesResponse{}})
		outPath := filepath.Join(t.TempDir(), "empty.json")

		data := runControl(t, "agent", "export", agID, "--style", "json", "--output", outPath).Data(t)
		assert.Equal(t, float64(0), data["messages"])

		raw, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Equal(t, "{\n  \"messages\": []\n}\n", string(raw))
	})

	t.Run("empty transcript exports cleanly across markdown, text, json, projected json, and ndjson", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{pages: map[int64]*leapmuxv1.ListAgentMessagesResponse{}})

		md := runControlText(t, "agent", "export", agID, "--style", "markdown")
		require.Equal(t, cli.ExitOK, md.Code, md.Stderr)
		assert.Empty(t, md.Stdout)

		txt := runControlText(t, "agent", "export", agID, "--style", "text")
		require.Equal(t, cli.ExitOK, txt.Code, txt.Stderr)
		assert.Empty(t, txt.Stdout)

		jsonOut := runControl(t, "agent", "export", agID)
		require.Equal(t, cli.ExitOK, jsonOut.Code, jsonOut.Stderr)
		assert.Equal(t, "{\"data\":{\"messages\":[]}}\n", jsonOut.Stdout)
		assert.Equal(t, []any{}, jsonOut.Data(t)["messages"])

		projected := runControl(t, "agent", "export", agID, "--fields", "seq").Data(t)
		assert.Equal(t, []any{}, projected["messages"])

		ndjsonOut := runControlText(t, "agent", "export", agID, "--format", "ndjson")
		require.Equal(t, cli.ExitOK, ndjsonOut.Code, ndjsonOut.Stderr)
		assert.Empty(t, ndjsonOut.Stdout)

		mdPath := filepath.Join(t.TempDir(), "empty.md")
		fileData := runControl(t, "agent", "export", agID, "--style", "markdown", "--output", mdPath).Data(t)
		assert.Equal(t, float64(0), fileData["messages"])
		raw, err := os.ReadFile(mdPath)
		require.NoError(t, err)
		assert.Empty(t, raw)
	})

	t.Run("markdown and text styles stream to stdout", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{pages: makePages()})

		md := runControlText(t, "agent", "export", agID, "--style", "markdown")
		require.Equal(t, cli.ExitOK, md.Code, md.Stderr)
		assert.Equal(t, 1, strings.Count(md.Stdout, "#2 agent"), "overlapping seq 2 must be emitted once")
		assert.Contains(t, md.Stdout, "### #1 user\n\nFirst reply\n\n### #2 agent\n\nSecond reply\n\n### #3 agent\n\nThird reply\n")

		txt := runControlText(t, "agent", "export", agID, "--style", "text")
		require.Equal(t, cli.ExitOK, txt.Code, txt.Stderr)
		assert.Equal(t, "#1 user: First reply\n#2 agent: Second reply\n#3 agent: Third reply\n", txt.Stdout)
	})

	t.Run("stdout --json and --format ndjson stream cleanly", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{pages: makePages()})

		jsonData := runControl(t, "agent", "export", agID).Data(t)
		msgs, ok := jsonData["messages"].([]any)
		require.True(t, ok)
		require.Len(t, msgs, 3)

		ndjsonOut := runControlText(t, "agent", "export", agID, "--format", "ndjson")
		require.Equal(t, cli.ExitOK, ndjsonOut.Code, ndjsonOut.Stderr)
		lines := strings.Split(strings.TrimSpace(ndjsonOut.Stdout), "\n")
		require.Len(t, lines, 3)
	})

	t.Run("RPC failure mid-export leaves no partial output or temp file", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{
			pages:           makePages(),
			pageErrAtCursor: map[int64]codes.Code{2: codes.Unavailable},
		})
		dir := t.TempDir()
		outPath := filepath.Join(dir, "partial.md")

		runControl(t, "agent", "export", agID, "--style", "markdown", "--output", outPath).
			requireFailure(t, cli.CodeUnavailable)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "failed export must leave neither partial target nor temp file")
	})
}

func TestRunAgentStartupRespond_ReusesOneChannelOverHub(t *testing.T) {
	agID := fullTestID("ag-start")
	w := &agentOpsDispatcher{
		agents: []*leapmuxv1.AgentInfo{{
			Id:            agID,
			StartupPrompt: &leapmuxv1.AgentStartupPrompt{PromptId: "prompt-42", Title: "Keychain"},
		}},
	}
	hub := &recordingHub{
		workers: workerRows("worker-A"),
		locateTab: &leapmuxv1.WorkspaceTab{
			Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agID},
			TileId:      "root-1",
			WorkerId:    "worker-A",
			WorkspaceId: "ws-1",
		},
	}
	h := startHubChannelHarness(t, hub, w)
	h.dispatcher.Handle(leapmuxv1connect.WorkerAgentServiceRespondAgentStartupPromptProcedure, channel.Registration{}, w.DispatchWith)

	data := runControl(t, "agent", "startup", "approve", agID, "--hub", h.url).Data(t)
	assert.Equal(t, agID, data["id"])
	assert.Equal(t, "prompt-42", data["prompt_id"])
	assert.Equal(t, true, data["proceed"])
	assert.Equal(t, 1, h.channelOpens(), "ListAgents and RespondAgentStartupPrompt must share one E2EE channel")
	require.Len(t, w.startupRequests, 1)
	assert.Equal(t, "prompt-42", w.startupRequests[0].GetPromptId())
}

func TestNewAgentOpsCommandMetadata(t *testing.T) {
	startup := newAgentStartupCommand()
	for _, subName := range []string{"approve", "cancel"} {
		sub, _, err := startup.Find([]string{subName})
		require.NoError(t, err)
		assert.NotNil(t, sub.Flags().Lookup("prompt-id"), "%s must register --prompt-id", subName)
		assert.NotNil(t, sub.Flags().Lookup("workspace"), "%s must register --workspace", subName)
	}

	marks := newAgentMarksCommand()
	assert.Contains(t, marks.Short, "sequence limits")
	assert.NotContains(t, marks.Short, "bounds")
}

func TestRunAgentRecoveries_UnknownProviderIncludesAcceptedValuesHint(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

	f := runControl(t, "agent", "recoveries", "--provider", "definitely-not-a-provider", "--hub", "https://stub.invalid").
		requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, `unknown --provider "definitely-not-a-provider"`)
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], "accepted values:")
	assert.Contains(t, f.Hints[0], "claude-code")
}

func TestAgentTranscriptCommands_RequestIncludeReplyText(t *testing.T) {
	agID := fullTestID("ag-transcript-reply")
	reply := "Worker extracted text"
	w := &agentOpsDispatcher{
		messages: []*leapmuxv1.AgentChatMessage{
			{
				Id:                 "m-1",
				Seq:                1,
				Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
				ReplyText:          &reply,
				Content:            []byte(`{"provider_specific":"opaque"}`),
				ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			},
		},
		pages: map[int64]*leapmuxv1.ListAgentMessagesResponse{
			0: {
				Messages: []*leapmuxv1.AgentChatMessage{
					{
						Id:                 "m-1",
						Seq:                1,
						Source:             leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
						ReplyText:          &reply,
						Content:            []byte(`{"provider_specific":"opaque"}`),
						ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
					},
				},
			},
		},
	}
	startAgentWorker(t, agID, w)

	msgOut := runControlText(t, "agent", "message", agID, "1")
	require.Equal(t, cli.ExitOK, msgOut.Code, msgOut.Stderr)
	assert.Equal(t, "#1 agent: Worker extracted text\n", msgOut.Stdout)
	require.Len(t, w.getMessageRequests, 1)
	assert.True(t, w.getMessageRequests[0].GetIncludeReplyText(), "agent message must set IncludeReplyText")

	spanOut := runControlText(t, "agent", "span", agID, "toolu_1")
	require.Equal(t, cli.ExitOK, spanOut.Code, spanOut.Stderr)
	assert.Equal(t, "#1 agent: Worker extracted text\n", spanOut.Stdout)
	require.Len(t, w.getSpanRequests, 1)
	assert.True(t, w.getSpanRequests[0].GetIncludeReplyText(), "agent span must set IncludeReplyText")

	rawSpanOut := runControlText(t, "agent", "span", agID, "toolu_1", "--session-id", " sess-9 ", "--raw")
	require.Equal(t, cli.ExitOK, rawSpanOut.Code, rawSpanOut.Stderr)
	assert.Contains(t, rawSpanOut.Stdout, `{"provider_specific":"opaque"}`)
	assert.NotContains(t, rawSpanOut.Stdout, "Worker extracted text")
	require.Len(t, w.getSpanRequests, 2)
	assert.True(t, w.getSpanRequests[1].GetIncludeReplyText(), "agent span --raw must still set IncludeReplyText")
	assert.Equal(t, "toolu_1", w.getSpanRequests[1].GetSpanId())
	assert.Equal(t, "sess-9", w.getSpanRequests[1].GetAgentSessionId())

	expOut := runControlText(t, "agent", "export", agID, "--style", "text")
	require.Equal(t, cli.ExitOK, expOut.Code, expOut.Stderr)
	assert.Equal(t, "#1 agent: Worker extracted text\n", expOut.Stdout)
	require.Len(t, w.listMessageRequests, 1)
	assert.True(t, w.listMessageRequests[0].GetIncludeReplyText(), "agent export must set IncludeReplyText")
}

func TestAgentTranscriptCommands_RejectInvalidInputsAndMissingMessage(t *testing.T) {
	agID := fullTestID("ag-transcript-err")

	t.Run("rejects blank span-id and non-positive seq before dialing", func(t *testing.T) {
		clearRemoteEnv(t)
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())

		f := runControl(t, "agent", "span", agID, "   ", "--hub", "https://stub.invalid").requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "<span-id> is empty")

		for _, badSeq := range []string{"0", "-1", "abc"} {
			f := runControlText(t, "agent", "message", "--json", "--hub", "https://stub.invalid", "--", agID, badSeq).requireFailure(t, cli.CodeUsage)
			assert.Contains(t, f.Message, "positive integer")
		}
	})

	t.Run("missing message in worker response returns not_found", func(t *testing.T) {
		startAgentWorker(t, agID, &agentOpsDispatcher{})
		f := runControl(t, "agent", "message", agID, "42").requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no message at seq 42")
	})
}

func TestRunAgentStartupRespond_ExplicitPromptIDAndMissingPrompt(t *testing.T) {
	agID := fullTestID("ag-start-edge")

	t.Run("explicit --prompt-id on cancel skips ListAgents", func(t *testing.T) {
		w := &agentOpsDispatcher{}
		startAgentWorker(t, agID, w)

		data := runControl(t, "agent", "startup", "cancel", agID, "--prompt-id", " prompt-99 ").Data(t)
		assert.Equal(t, agID, data["id"])
		assert.Equal(t, "prompt-99", data["prompt_id"])
		assert.Equal(t, false, data["proceed"])
		assert.NotContains(t, w.recordedCalls(), leapmuxv1connect.WorkerAgentServiceListAgentsProcedure)
		require.Len(t, w.startupRequests, 1)
		assert.Equal(t, agID, w.startupRequests[0].GetAgentId())
		assert.Equal(t, "prompt-99", w.startupRequests[0].GetPromptId())
		assert.False(t, w.startupRequests[0].GetProceed())
	})

	t.Run("missing pending startup prompt returns not_found", func(t *testing.T) {
		w := &agentOpsDispatcher{
			agents: []*leapmuxv1.AgentInfo{{Id: agID}},
		}
		startAgentWorker(t, agID, w)

		f := runControl(t, "agent", "startup", "approve", agID).requireFailure(t, cli.CodeNotFound)
		assert.Contains(t, f.Message, "no pending startup prompt")
		assert.Empty(t, w.startupRequests)
	})
}
