package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

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
		TabPayloadRegistered: &leapmuxv1.TabPayloadRegistered{TabId: "f1"},
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

func mustScopes(tokens string) authscope.ScopeSet {
	set, err := authscope.Parse(tokens)
	if err != nil {
		panic(err)
	}
	return set
}
