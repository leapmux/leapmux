package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/util/version"
)

// GetWorkerSystemInfo reports the worker's build in the BuildInfo message that
// the hub's system information carries, so both read through one builder.
func TestGetWorkerSystemInfoReportsTheBuild(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	dispatch(d, leapmuxv1connect.WorkerSystemServiceGetWorkerSystemInfoProcedure, &leapmuxv1.GetWorkerSystemInfoRequest{}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.GetWorkerSystemInfoResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Equal(t, svc.Name, resp.GetName())
	require.NotNil(t, resp.GetBuild(), "the reply carries the build message")
	assert.True(t, proto.Equal(version.BuildInfoProto(), resp.GetBuild()),
		"the build is the running binary's: got %v", resp.GetBuild())
}
