package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func TestControlRejectsConflictingOutputFormatsBeforeRequests(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "--quiet", "worker", "list"},
		{"worker", "list", "--quiet", "--json"},
	} {
		hub := &recordingHub{workers: []*leapmuxv1.Worker{}}
		startSpawnIPC(t, hub, nil)
		out := runControlText(t, args...)
		failure := out.requireFailure(t, cli.CodeUsage)
		assert.Contains(t, failure.Message, "--json and --quiet")
		assert.Equal(t, cli.ExitUsage, out.Code)
		assert.Empty(t, hub.called())
	}
}

func TestControlHonorsTheLastExplicitOutputFlagValue(t *testing.T) {
	jsonOutput := runControlText(t, "guide", "--quiet", "--quiet=false", "--json")
	require.Equal(t, cli.ExitOK, jsonOutput.Code, jsonOutput.Stderr)
	assert.Contains(t, jsonOutput.Data(t)["guide"], "Driving LeapMux")

	quietOutput := runControlText(t, "guide", "--topics", "--json", "--json=false", "-q")
	require.Equal(t, cli.ExitOK, quietOutput.Code, quietOutput.Stderr)
	assert.Contains(t, quietOutput.Stdout, "targets\n")
	assert.NotContains(t, quietOutput.Stdout, "{\"data\"")
}

func TestWorkspaceCreatePrintsAlignedKeyValueLines(t *testing.T) {
	hub := &rpcHub{answers: map[string]proto.Message{
		leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure: &leapmuxv1.CreateWorkspaceResponse{WorkspaceId: "created-workspace"},
	}}
	startRPCIPC(t, hub, nil)
	out := runControlText(t, "workspace", "create", "Nightly\nreview")
	require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
	assert.Equal(t, "id:    created-workspace\ntitle: Nightly\n       review\n", out.Stdout)
}

func TestFormatTimeAndWallTime(t *testing.T) {
	assert.Empty(t, formatTime(nil))
	assert.Empty(t, formatWallTime(time.Time{}))

	loc := time.FixedZone("KST", 9*3600)
	ts := time.Date(2026, 11, 27, 20, 15, 30, 123000000, loc)
	want := timefmt.Format(ts)
	assert.Equal(t, "2026-11-27T11:15:30.123Z", want)
	assert.Equal(t, want, formatWallTime(ts))
	assert.Equal(t, want, formatTime(timestamppb.New(ts)))
}
