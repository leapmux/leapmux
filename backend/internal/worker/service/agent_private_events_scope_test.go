package service

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// TestPrivateEventFixturesStateAKindOnEveryHost pins the precondition every
// case below depends on, against the SAME tabPayloadType the code under test
// calls: each fixture payload must state a kind on the running host.
//
// privateEventVisible fails closed on a payload it cannot classify, so a
// fixture the host rejects makes a visibility assertion fail while reporting
// the scope gate -- the behavior the test exists to pin and the one part of it
// that is correct. This case reports the fixture instead.
func TestPrivateEventFixturesStateAKindOnEveryHost(t *testing.T) {
	t.Parallel()

	fileKind, err := tabPayloadType(filePayloadFor("/repo/a.txt"))
	require.NoErrorf(t, err, "the FILE fixture must state a kind on %s", runtime.GOOS)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_FILE, fileKind)

	imageKind, err := tabPayloadType(imagePayloadFor("agent-a", 42, 0, "mcp__playwright__screenshot"))
	require.NoErrorf(t, err, "the IMAGE fixture must state a kind on %s", runtime.GOOS)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_IMAGE, imageKind)
}

// A tab rename's title is exactly what the agent:read / terminal:read scopes
// govern, and the private-events bus multiplexes those renames beside the
// file-tab events. A caller granted file:read alone must keep the file-tab
// events and hear nothing about agents or terminals it holds no scope to read
// -- the same partition WatchEvents applies to its sections.
func TestPrivateEventVisibleGatesRenamesByTabKind(t *testing.T) {
	fileOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read")}
	agentAndFile := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read agent:read")}

	agentRename := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
		TabRenamed: &leapmuxv1.TabRenamed{TabId: "a1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, Title: "secret title"},
	}}
	terminalRename := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
		TabRenamed: &leapmuxv1.TabRenamed{TabId: "t1", TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, Title: "secret title"},
	}}
	fileEvent := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{TabId: "f1", Payload: filePayloadFor("/repo/a.txt")},
	}}

	assert.False(t, privateEventVisible(fileOnly, agentRename),
		"a file:read caller must not read agent tab titles")
	assert.False(t, privateEventVisible(fileOnly, terminalRename),
		"a file:read caller must not read terminal tab titles")
	assert.True(t, privateEventVisible(fileOnly, fileEvent),
		"file-tab events ride the stream's own floor")

	assert.True(t, privateEventVisible(agentAndFile, agentRename),
		"an agent:read caller reads agent tab titles")
}

// An IMAGE payload states an agent id, a message seq and a tool-derived title.
// Those are facts about a transcript, which is what agent:read governs -- and
// file:read does not imply it (contracts/scopes.json gives SCOPE_FILE_READ only
// SCOPE_WORKER_READ). The stream is gated on file:read alone, so without a
// per-kind gate every IMAGE row of the bootstrap replay told a file:read-only
// caller which agents exist and which messages the user opened.
func TestPrivateEventVisibleGatesImagePayloadsByAgentScope(t *testing.T) {
	fileOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read")}
	agentAndFile := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read agent:read")}

	imageEvent := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{
			TabId:   "i1",
			Payload: imagePayloadFor("agent-a", 42, 0, "mcp__playwright__screenshot"),
		},
	}}
	fileEvent := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{TabId: "f1", Payload: filePayloadFor("/repo/a.txt")},
	}}

	assert.False(t, privateEventVisible(fileOnly, imageEvent),
		"a file:read caller must not learn an image tab's agent, seq or title")
	assert.True(t, privateEventVisible(agentAndFile, imageEvent),
		"an agent:read caller reads image payloads")
	assert.True(t, privateEventVisible(fileOnly, fileEvent),
		"a FILE payload still rides the stream's own file:read floor")
}

// A payload this binary cannot parse states no kind, so it states no scope
// either. Failing closed is the only answer that cannot leak: the alternative
// hands an unknown future payload to whichever caller happens to be listening.
func TestPrivateEventVisibleRefusesAnUndecodablePayload(t *testing.T) {
	fileOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read")}
	everything := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read agent:read terminal:read")}

	noKind := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRegistered{
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{TabId: "x1", Payload: &leapmuxv1.TabPayload{}},
	}}

	assert.False(t, privateEventVisible(fileOnly, noKind))
	assert.False(t, privateEventVisible(everything, noKind),
		"a payload with no kind is withheld from every caller, not just an unprivileged one")
}

// The revoke event carries a bare tab id and no payload, so it states nothing
// about an agent and needs no per-kind gate. It must keep flowing, or a peer
// that closed an image tab would leave a phantom on every other client.
func TestPrivateEventVisiblePassesRevocations(t *testing.T) {
	fileOnly := channel.Caller{UserID: userid.MustNew("u1"), Scopes: mustScopes("file:read")}
	revoked := &leapmuxv1.WorkerPrivateEvent{Event: &leapmuxv1.WorkerPrivateEvent_TabPayloadRevoked{
		TabPayloadRevoked: &leapmuxv1.TabPayloadRevoked{TabId: "i1"},
	}}
	assert.True(t, privateEventVisible(fileOnly, revoked))
}

// filePayloadFor builds a FILE payload from a POSIX-style path literal.
//
// The literal goes through testutil.NativeAbsPath because privateEventVisible
// reaches tabPayloadType, which asks filepath.IsAbs whether the file_path is
// absolute. That question is platform-specific: "/repo/a.txt" is NOT absolute
// on Windows, so a raw literal makes tabPayloadType fail closed and every
// assertion below that expects a visible FILE event fails there for a reason
// none of them is about. TestPrivateEventFixturesStateAKindOnEveryHost pins it.
func filePayloadFor(posixPath string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: testutil.NativeAbsPath("/repo"),
		Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{
			FilePath: testutil.NativeAbsPath(posixPath),
		}},
	}
}

func imagePayloadFor(agentID string, seq int64, index int32, title string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: testutil.NativeAbsPath("/repo"),
		Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{
			AgentId: agentID, Seq: seq, ImageIndex: index, Title: title,
		}},
	}
}

func mustScopes(tokens string) authscope.ScopeSet {
	set, err := authscope.Parse(tokens)
	if err != nil {
		panic(err)
	}
	return set
}
