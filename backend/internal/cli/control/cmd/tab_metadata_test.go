package cmd

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

type tabMetadataDispatcher struct {
	mu             sync.Mutex
	agentRequests  [][]string
	viewerRequests []string
	agents         []*leapmuxv1.AgentInfo
}

func (d *tabMetadataDispatcher) DispatchWith(_ context.Context, _ channel.Caller, request *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	switch request.GetMethod() {
	case leapmuxv1connect.WorkerAgentServiceListAgentsProcedure:
		var input leapmuxv1.ListAgentsRequest
		if err := proto.Unmarshal(request.GetPayload(), &input); err != nil {
			_ = writer.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.agentRequests = append(d.agentRequests, slices.Clone(input.GetTabIds()))
		d.mu.Unlock()
		agents := d.agents
		if agents == nil {
			agents = []*leapmuxv1.AgentInfo{
				{Id: "agent-alpha", Title: "First review", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, ActivityState: idle},
				{Id: "agent-beta", Title: "Second review", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ActivityState: working},
			}
		}
		writeProtoResponse(writer, &leapmuxv1.ListAgentsResponse{Agents: agents})
	case leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure:
		writeProtoResponse(writer, &leapmuxv1.GetWorkerSystemInfoResponse{Name: "build-box"})
	case leapmuxv1connect.WorkerPrivateServiceGetTabPayloadProcedure:
		var input leapmuxv1.GetTabPayloadRequest
		if err := proto.Unmarshal(request.GetPayload(), &input); err != nil {
			_ = writer.SendError(int32(codes.InvalidArgument), err.Error())
			return
		}
		d.mu.Lock()
		d.viewerRequests = append(d.viewerRequests, input.GetTabId())
		d.mu.Unlock()
		payload := &leapmuxv1.TabPayload{}
		switch input.GetTabId() {
		case "file-win":
			payload.Kind = &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: `C:\repo\readme.md`}}
		case "file-posix":
			payload.Kind = &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: `/src/notes\archived.md`}}
		case "image-chart":
			payload.Kind = &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{Title: "Chart "}}
		default:
			_ = writer.SendError(int32(codes.NotFound), "no viewer payload")
			return
		}
		writeProtoResponse(writer, &leapmuxv1.GetTabPayloadResponse{Payload: payload})
	default:
		_ = writer.SendError(int32(codes.Unimplemented), "unexpected method: "+request.GetMethod())
	}
}

func TestRunTabList_UsesBatchedAgentDetailsAndWorkerViewerTitles(t *testing.T) {
	hub := &recordingHub{materialized: newStateBuilder().
		workspace("ws-1", "root-1").
		leafNode("root-1", "", "m").
		tab("agent-alpha", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("agent-beta", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
		tab("file-win", "root-1", leapmuxv1.TabType_TAB_TYPE_FILE).
		tab("file-posix", "root-1", leapmuxv1.TabType_TAB_TYPE_FILE).
		tab("image-chart", "root-1", leapmuxv1.TabType_TAB_TYPE_IMAGE).st}
	dispatcher := &tabMetadataDispatcher{}
	startSpawnIPC(t, hub, dispatcher)
	data := runControl(t, "tab", "list").Data(t)
	list, ok := data["tabs"].([]any)
	require.True(t, ok)
	require.Len(t, list, 5)
	rows := make(map[string]map[string]any, len(list))
	for _, item := range list {
		row := item.(map[string]any)
		rows[row["id"].(string)] = row
	}
	assert.Equal(t, "codex", rows["agent-alpha"]["provider"])
	assert.Equal(t, "claude_code", rows["agent-beta"]["provider"])
	assert.Equal(t, "working", rows["agent-beta"]["state"])
	assert.Equal(t, "build-box", rows["agent-alpha"]["worker"])
	assert.Equal(t, "readme.md", rows["file-win"]["title"])
	assert.Equal(t, `notes\archived.md`, rows["file-posix"]["title"])
	assert.Equal(t, "Chart ", rows["image-chart"]["title"])

	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	require.Len(t, dispatcher.agentRequests, 1)
	assert.ElementsMatch(t, []string{"agent-alpha", "agent-beta"}, dispatcher.agentRequests[0])
	assert.ElementsMatch(t, []string{"file-win", "file-posix", "image-chart"}, dispatcher.viewerRequests)
}

func TestViewerTabTitleDistinguishesEmptyAndUnknownPayloads(t *testing.T) {
	file := &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{}}}
	image := &leapmuxv1.TabPayload{Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{}}}
	for _, tc := range []struct {
		name    string
		kind    leapmuxv1.TabType
		payload *leapmuxv1.TabPayload
		title   string
		known   bool
	}{
		{name: "missing payload", kind: leapmuxv1.TabType_TAB_TYPE_FILE},
		{name: "wrong payload", kind: leapmuxv1.TabType_TAB_TYPE_FILE, payload: image},
		{name: "empty file path", kind: leapmuxv1.TabType_TAB_TYPE_FILE, payload: file, title: "File", known: true},
		{name: "empty image title", kind: leapmuxv1.TabType_TAB_TYPE_IMAGE, payload: image, title: "Image", known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, known := viewerTabTitle(tc.kind, tc.payload)
			assert.Equal(t, tc.title, title)
			assert.Equal(t, tc.known, known)
		})
	}
}
