package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func requireRootOutputSink(t testing.TB, output *OutputHandler, agentID string) *agentOutputSink {
	t.Helper()
	value, ok := output.rootSinks.Load(agentID)
	require.True(t, ok, "root output sink %q is absent", agentID)
	return value.(*agentOutputSink)
}

func requireChildOutputSink(t testing.TB, parent *agentOutputSink, childAgentID string) *agentOutputSink {
	t.Helper()
	parent.ChildSink(childAgentID)
	parent.childMu.Lock()
	defer parent.childMu.Unlock()
	child := parent.childSinks[childAgentID]
	require.NotNil(t, child, "child output sink %q is absent", childAgentID)
	return child
}
