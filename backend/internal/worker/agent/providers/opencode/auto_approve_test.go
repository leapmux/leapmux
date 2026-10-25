package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The options of an ordinary permission request of the family.
const familyPermissionOptions = `[{"optionId":"once","kind":"allow_once","name":"Allow once"},{"optionId":"always","kind":"allow_always","name":"Always allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]`

// The options of a Kilo request that Kilo reserves for a person: skill shell
// commands, or a command that leaves the sandbox. It offers no Always.
const kiloTemporaryPermissionOptions = `[{"optionId":"once","kind":"allow_once","name":"Allow"},{"optionId":"reject","kind":"reject_once","name":"Reject"}]`

func newAutoApproveAgent(t *testing.T, autoApprove bool) (*Agent, *agenttest.ControlSink, *agenttest.Stdin) {
	t.Helper()
	stdin := &agenttest.Stdin{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &Agent{}
	a.AttachPeerForTest(ctx, cancel, stdin, "test-agent", "session-1")
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
	a.setAutoApprove(autoApprove)
	return a, sink, stdin
}

func requestPermission(a *Agent, id int, options string) {
	a.HandleOutput(fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call-%d","title":"bash"},"options":%s}}`, id, id, options))
}

// permissionAnswers reads the answer that the agent wrote for each request id,
// after every write that the agent queued before the call.
//
// The agent writes its answers through one ordered queue. The barrier sends a
// request of its own through that queue, so once that request reaches stdin,
// each answer queued before it is there as well.
func permissionAnswers(t *testing.T, a *Agent, stdin *agenttest.Stdin) map[string]string {
	t.Helper()
	previous := barrierRequestIDs(stdin)
	sent := make(chan error, 1)
	go func() {
		_, err := a.SendRequest(writeBarrierMethod, nil, 30*time.Second)
		sent <- err
	}()
	var barrierID int64
	testutil.RequireEventually(t, func() bool {
		ids := barrierRequestIDs(stdin)
		if len(ids) == len(previous) {
			return false
		}
		barrierID = ids[len(ids)-1]
		return true
	}, "the barrier request reaches stdin")
	require.True(t, a.Deliver(barrierID, agenttest.JSONRPCResponse(barrierID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	require.NoError(t, <-sent)
	// The barrier is a request, which the reader skips, and its reply went to
	// the agent, not to stdin.
	return agenttest.JSONRPCResultsByID(t, stdin.String())
}

// barrierRequestIDs lists the ids of the barrier requests on stdin, in order.
func barrierRequestIDs(stdin *agenttest.Stdin) []int64 {
	var ids []int64
	for _, line := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal([]byte(line), &request) == nil && request.Method == writeBarrierMethod {
			ids = append(ids, request.ID)
		}
	}
	return ids
}

// writeBarrierMethod is the method of the barrier request in permissionAnswers.
const writeBarrierMethod = "test/barrier"

const approvedOnce = `{"outcome":{"outcome":"selected","optionId":"once"}}`

func TestAutoApproveAnswersEachPermissionOnce(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newAutoApproveAgent(t, true)
	requestPermission(a, 7, familyPermissionOptions)
	requestPermission(a, 8, familyPermissionOptions)
	answers := permissionAnswers(t, a, stdin)
	assert.JSONEq(t, approvedOnce, answers["7"], "Auto-approve selects the once option, never the lasting rule")
	assert.JSONEq(t, approvedOnce, answers["8"])
	assert.Empty(t, sink.PublishedControls(), "the reader sees no prompt")
}

func TestAutoApproveOffAsksTheReader(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newAutoApproveAgent(t, false)
	requestPermission(a, 7, familyPermissionOptions)
	assert.Empty(t, permissionAnswers(t, a, stdin))
	assert.Len(t, sink.PublishedControls(), 1)
}

func TestAutoApproveLeavesARequestWithoutAlwaysToTheReader(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newAutoApproveAgent(t, true)
	requestPermission(a, 7, kiloTemporaryPermissionOptions)
	assert.Empty(t, permissionAnswers(t, a, stdin), "Kilo reserves this request for a person")
	assert.Len(t, sink.PublishedControls(), 1)
}

func TestAutoApproveChangesDuringASession(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newAutoApproveAgent(t, false)
	requestPermission(a, 7, familyPermissionOptions)
	require.Len(t, sink.PublishedControls(), 1)
	require.Empty(t, permissionAnswers(t, a, stdin))

	// Turning the option on approves the request that already waits.
	a.UpdateSettings(optionmap.Map{contracts.OpenCodeOptionAutoApprove: "true"})
	assert.JSONEq(t, approvedOnce, permissionAnswers(t, a, stdin)["7"])
	assert.Equal(t, "true", optionids.GroupByID(a.OptionGroups(), contracts.OpenCodeOptionAutoApprove).GetCurrentValue())

	// Turning it off asks the reader again.
	a.UpdateSettings(optionmap.Map{contracts.OpenCodeOptionAutoApprove: "false"})
	requestPermission(a, 8, familyPermissionOptions)
	assert.NotContains(t, permissionAnswers(t, a, stdin), "8")
	assert.Len(t, sink.PublishedControls(), 2)
	assert.Equal(t, "false", optionids.GroupByID(a.OptionGroups(), contracts.OpenCodeOptionAutoApprove).GetCurrentValue())
}

func TestAutoApproveRefusesAnInvalidValue(t *testing.T) {
	t.Parallel()
	a, _, _ := newAutoApproveAgent(t, true)
	result := a.UpdateSettings(optionmap.Map{contracts.OpenCodeOptionAutoApprove: "yes"})
	assert.Equal(t, "true", result.ConfirmedOptions()[contracts.OpenCodeOptionAutoApprove], "the refusal keeps the running value and the process")
	assert.Equal(t, "true", optionids.GroupByID(a.OptionGroups(), contracts.OpenCodeOptionAutoApprove).GetCurrentValue())
}

func TestAutoApproveLocalOption(t *testing.T) {
	t.Parallel()
	b := &FamilyBase{}
	groups := b.localOptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, contracts.OpenCodeOptionAutoApprove, groups[0].GetId())
	assert.Equal(t, "false", groups[0].GetCurrentValue())
	assert.Equal(t, []string{"false", "true"}, optionIDs(groups[0]))

	handled, err := b.applyLocalOption("model", "true")
	assert.False(t, handled, "another group belongs to the session")
	require.NoError(t, err)
	handled, err = b.applyLocalOption(contracts.OpenCodeOptionAutoApprove, "maybe")
	assert.True(t, handled)
	require.Error(t, err)
	assert.Equal(t, "false", b.localOptionGroups()[0].GetCurrentValue())
	handled, err = b.applyLocalOption(contracts.OpenCodeOptionAutoApprove, "true")
	assert.True(t, handled)
	require.NoError(t, err)
	assert.Equal(t, "true", b.localOptionGroups()[0].GetCurrentValue())
}

func TestWithAutoApproveClaimsUnderTheSettingsLock(t *testing.T) {
	t.Parallel()
	a, _, _ := newAutoApproveAgent(t, true)
	claims := 0
	claim := func() {
		claims++
		unlocked := a.Mu.TryLock()
		if unlocked {
			a.Mu.Unlock()
		}
		assert.False(t, unlocked, "a change of the option cannot pass between the check and the approval")
	}
	a.withAutoApprove(claim)
	a.setAutoApprove(false)
	a.withAutoApprove(claim)
	assert.Equal(t, 1, claims)
}

func TestPermissionOffersAlways(t *testing.T) {
	t.Parallel()
	for raw, offers := range map[string]bool{
		`{"options":` + familyPermissionOptions + `}`:        true,
		`{"options":` + kiloTemporaryPermissionOptions + `}`: false,
		`{"options":[]}`:              false,
		`{}`:                          false,
		`not JSON`:                    false,
		`{"options":"allow_always"}`:  false,
		`{"options":[{"kind":null}]}`: false,
	} {
		line := &providerkit.ParsedLine{Params: []byte(raw)}
		assert.Equal(t, offers, permissionOffersAlways(line), raw)
	}
}

func TestStartRefusesAnInvalidAutoApprove(t *testing.T) {
	t.Parallel()
	_, err := Start(t.Context(), agent.Options{AgentID: "opencode-invalid", Options: map[string]string{contracts.OpenCodeOptionAutoApprove: "yes"}}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto-approve")
}

func TestRegistrationServesAutoApprove(t *testing.T) {
	t.Parallel()
	group := optionids.GroupByID(Registration().OptionGroups, contracts.OpenCodeOptionAutoApprove)
	assert.Same(t, AutoApproveGroup, group)
	assert.Equal(t, "false", group.GetDefaultValue())
	assert.True(t, group.GetMutable())
	assert.NotNil(t, optionids.GroupByID(Registration().OptionGroups, "primaryAgent"), "the member keeps its own groups")
}

func optionIDs(group *leapmuxv1.AvailableOptionGroup) []string {
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}
