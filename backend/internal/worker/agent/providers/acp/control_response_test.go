package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestACPResolveControlResponse_PreservesButWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, Provider{})
}

func TestACPResolveControlResponse_PreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, Provider{})
}
