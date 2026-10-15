//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// autoReviewStub implements the Agent provider surface (via stubProvider) plus
// AutoReviewDenialApprover, so Manager.ApproveAutoReviewDenial can reach it.
type autoReviewStub struct {
	stubProvider
	entries  []json.RawMessage
	reviewID string
	calls    int
	err      error
}

func (s *autoReviewStub) ApproveAutoReviewDenial(entries []json.RawMessage, reviewID string) error {
	s.calls++
	s.entries = entries
	s.reviewID = reviewID
	return s.err
}

var (
	_ agent.AutoReviewDenialApprover = (*autoReviewStub)(nil)
	_ agent.Agent                    = (*autoReviewStub)(nil)
)

func TestManagerAutoReview_ApproveOnAnAgentThatIsNotRunning(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)

	err := m.ApproveAutoReviewDenial("nope", nil, "review-1")

	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

// A provider without an automatic reviewer implements no approver, and the Manager
// must refuse rather than panic on the type assertion.
func TestManagerAutoReview_ApproveOnAProviderWithNoReviewer(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})

	err := m.ApproveAutoReviewDenial("root", nil, "review-1")

	assert.ErrorIs(t, err, agent.ErrAutoReviewApprovalUnsupported)
}

func TestManagerAutoReview_ApproveReachesTheRunningProvider(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &autoReviewStub{}
	m.PutAgentForTest("root", st)
	entries := []json.RawMessage{json.RawMessage(`{"method":"x"}`)}

	require.NoError(t, m.ApproveAutoReviewDenial("root", entries, "review-1"))

	assert.Equal(t, 1, st.calls)
	assert.Equal(t, entries, st.entries)
	assert.Equal(t, "review-1", st.reviewID)
}

func TestManagerAutoReview_ApproveReturnsTheProviderRefusal(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	refusal := errors.New("codex refused")
	m.PutAgentForTest("root", &autoReviewStub{err: refusal})

	err := m.ApproveAutoReviewDenial("root", nil, "review-1")

	assert.ErrorIs(t, err, refusal)
}
