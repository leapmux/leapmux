package providerkit

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWorkspaceTrustHost is an agent's trusted-folder list that a test scripts.
type fakeWorkspaceTrustHost struct {
	trusted    bool
	checkErr   error
	trustErr   error
	checks     int
	trustCalls int
}

func (h *fakeWorkspaceTrustHost) WorkspaceTrusted() (bool, error) {
	h.checks++
	return h.trusted, h.checkErr
}

func (h *fakeWorkspaceTrustHost) TrustWorkspace() error {
	h.trustCalls++
	return h.trustErr
}

// workspaceTrustAnswer frames the answer that the worker forwards.
func workspaceTrustAnswer(t *testing.T, requestID, decision string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"response": map[string]any{
		"request_id": requestID, "response": map[string]string{contracts.WorkspaceTrustFieldDecision: decision},
	}})
	require.NoError(t, err)
	return raw
}

func newWorkspaceTrustSink() (*agenttest.ControlSink, agent.ProviderServices) {
	sink := &agenttest.ControlSink{}
	return sink, agent.NewProviderServices(sink)
}

type workspaceTrustPublicationBarrier struct {
	agenttest.ControlSink
	entered chan struct{}
	release chan struct{}
}

func (s *workspaceTrustPublicationBarrier) PublishControlRequest(request agent.ControlRequest) error {
	s.entered <- struct{}{}
	<-s.release
	return s.ControlSink.PublishControlRequest(request)
}

type workspaceTrustCheckBarrier struct {
	fakeWorkspaceTrustHost
	entered chan struct{}
	release chan struct{}
}

func (h *workspaceTrustCheckBarrier) WorkspaceTrusted() (bool, error) {
	h.entered <- struct{}{}
	<-h.release
	return h.fakeWorkspaceTrustHost.WorkspaceTrusted()
}

type workspaceTrustAnswerBarrier struct {
	fakeWorkspaceTrustHost
	entered chan struct{}
	release chan struct{}
}

func (h *workspaceTrustAnswerBarrier) TrustWorkspace() error {
	err := h.fakeWorkspaceTrustHost.TrustWorkspace()
	h.entered <- struct{}{}
	<-h.release
	return err
}

type workspaceTrustAnswerResult struct {
	handled bool
	err     error
}

type workspaceTrustInstanceSink struct {
	agenttest.ControlSink
	mu                  sync.Mutex
	active              map[string]agent.ControlRequest
	publishCalls        int
	cancelCalls         int
	publishStarted      chan string
	cancelStarted       chan string
	releaseFirstPublish chan struct{}
	releaseFirstCancel  chan struct{}
}

func newWorkspaceTrustInstanceSink() *workspaceTrustInstanceSink {
	return &workspaceTrustInstanceSink{
		active:              make(map[string]agent.ControlRequest),
		publishStarted:      make(chan string, 8),
		cancelStarted:       make(chan string, 8),
		releaseFirstPublish: make(chan struct{}),
		releaseFirstCancel:  make(chan struct{}),
	}
}

func (s *workspaceTrustInstanceSink) PublishControlRequest(request agent.ControlRequest) error {
	s.mu.Lock()
	s.publishCalls++
	first := s.publishCalls == 1
	s.mu.Unlock()
	s.publishStarted <- request.RequestID
	if first {
		<-s.releaseFirstPublish
	}
	if err := s.ControlSink.PublishControlRequest(request); err != nil {
		return err
	}
	s.mu.Lock()
	s.active[request.RequestID] = request
	s.mu.Unlock()
	return nil
}

func (s *workspaceTrustInstanceSink) CancelControlRequest(requestID string) {
	s.cancel(requestID, "", false)
}

func (s *workspaceTrustInstanceSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.cancel(requestID, instanceID, true)
}

func (s *workspaceTrustInstanceSink) cancel(requestID, instanceID string, fenced bool) {
	s.mu.Lock()
	s.cancelCalls++
	first := s.cancelCalls == 1
	s.mu.Unlock()
	if first {
		s.cancelStarted <- requestID
		<-s.releaseFirstCancel
	}
	s.mu.Lock()
	current, ok := s.active[requestID]
	if ok && (!fenced || current.InstanceID == instanceID) {
		delete(s.active, requestID)
	}
	s.mu.Unlock()
	if fenced {
		s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
		return
	}
	s.ControlSink.CancelControlRequest(requestID)
}

func (s *workspaceTrustInstanceSink) activeRequest(requestID string) (agent.ControlRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.active[requestID]
	return request, ok
}

func (s *workspaceTrustInstanceSink) publishedCount(requestID string) int {
	count := 0
	for _, request := range s.PublishedControls() {
		if request.RequestID == requestID {
			count++
		}
	}
	return count
}

func awaitWorkspaceTrustReplacement(t *testing.T, trust *WorkspaceTrust, _ *workspaceTrustInstanceSink, requestID string) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	ready := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		trust.mu.Lock()
		defer trust.mu.Unlock()
		cond := trust.conditionLocked()
		for {
			select {
			case <-stop:
				return
			default:
			}
			state := trust.requestStates[requestID]
			if state != nil && state.waiting > 0 {
				close(ready)
				return
			}
			cond.Wait()
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		close(stop)
		trust.mu.Lock()
		trust.conditionLocked().Broadcast()
		trust.mu.Unlock()
		t.Fatal("the replacement workspace-trust question did not reach the publication fence")
	}
}

// Retire can run after the question registration but before the sink stores its
// card. The publication must then retire the card it created.
func TestWorkspaceTrustRetireDuringPublicationCancelsThePublishedQuestion(t *testing.T) {
	t.Parallel()
	sink := &workspaceTrustPublicationBarrier{entered: make(chan struct{}, 1), release: make(chan struct{})}
	services := agent.NewProviderServices(sink)
	release := sync.OnceFunc(func() { close(sink.release) })
	defer release()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	asked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", nil)
		close(asked)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-sink.entered:
	case <-ctx.Done():
		t.Fatal("the workspace-trust publication did not start")
	}

	retired := make(chan struct{})
	go func() {
		trust.Retire(services)
		close(retired)
	}()
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("retiring the workspace-trust question did not finish")
	}
	sink.ResetCanceledControls()
	release()
	select {
	case <-asked:
	case <-ctx.Done():
		t.Fatal("the workspace-trust publication did not finish")
	}
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, []string{WorkspaceTrustRequestID("session-1")}, sink.CanceledControls(),
		"a cancellation that precedes the visible card cannot retire it")
}

// Retire can run while the host checks the folder, before the question reaches
// the open set. That check belongs to the retired session and must not publish.
func TestWorkspaceTrustRetireDuringPrecheckPreventsPublication(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &workspaceTrustCheckBarrier{entered: make(chan struct{}, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(host.release) })
	defer release()
	var trust WorkspaceTrust
	asked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", nil)
		close(asked)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-host.entered:
	case <-ctx.Done():
		t.Fatal("the workspace-trust precheck did not start")
	}

	retired := make(chan struct{})
	go func() {
		trust.Retire(services)
		close(retired)
	}()
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("retiring the workspace-trust precheck did not finish")
	}
	release()
	select {
	case <-asked:
	case <-ctx.Done():
		t.Fatal("the workspace-trust precheck did not finish")
	}
	assert.Zero(t, sink.PublishedControlCount(), "a retired precheck must not publish its stale question")
	assert.Empty(t, sink.CanceledControls())

	trust.Ask(services, host, "session-2", "/work/project", nil)
	assert.Equal(t, 1, sink.PublishedControlCount(), "retirement must not refuse a later session")
	assert.Equal(t, WorkspaceTrustRequestID("session-2"), sink.LastPublishedControl().RequestID)
	assert.Equal(t, 2, host.checks)
}

// Only the first of two overlapping Trust answers may call the host and retire
// the question. The other answer observes a closed question immediately.
func TestWorkspaceTrustConcurrentTrustAnswersRunOnce(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &workspaceTrustAnswerBarrier{entered: make(chan struct{}, 2), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(host.release) })
	defer release()
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", nil)
	raw := workspaceTrustAnswer(t, WorkspaceTrustRequestID("session-1"), contracts.WorkspaceTrustDecisionTrust)
	first := make(chan workspaceTrustAnswerResult, 1)
	go func() {
		handled, err := trust.Answer(services, host, raw)
		first <- workspaceTrustAnswerResult{handled: handled, err: err}
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-host.entered:
	case <-ctx.Done():
		t.Fatal("the first Trust answer did not reach the host")
	}

	second := make(chan workspaceTrustAnswerResult, 1)
	go func() {
		handled, err := trust.Answer(services, host, raw)
		second <- workspaceTrustAnswerResult{handled: handled, err: err}
	}()
	select {
	case result := <-second:
		assert.True(t, result.handled)
		require.ErrorIs(t, result.err, ErrWorkspaceTrustClosed)
	case <-ctx.Done():
		t.Fatal("the second Trust answer did not finish while the first one was blocked")
	}
	release()
	select {
	case result := <-first:
		assert.True(t, result.handled)
		require.NoError(t, result.err)
	case <-ctx.Done():
		t.Fatal("the first Trust answer did not finish")
	}
	assert.Equal(t, 1, host.trustCalls)
	assert.Equal(t, []string{WorkspaceTrustRequestID("session-1")}, sink.CanceledControls())
}

// An old publication and Retire's cancellation must both finish before a new
// question with the same request ID reaches the sink. The replacement then stays
// stored and answerable, while another session remains independent.
func TestWorkspaceTrustOldPublicationAndRetirementCannotCancelReplacement(t *testing.T) {
	t.Parallel()
	sink := newWorkspaceTrustInstanceSink()
	services := agent.NewProviderServices(sink)
	releasePublish := sync.OnceFunc(func() { close(sink.releaseFirstPublish) })
	releaseCancel := sync.OnceFunc(func() { close(sink.releaseFirstCancel) })
	defer releasePublish()
	defer releaseCancel()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	requestID := WorkspaceTrustRequestID("session-1")
	ctx := testutil.DeadlineContext(t)
	oldAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", []string{"old"})
		close(oldAsked)
	}()
	select {
	case <-sink.publishStarted:
	case <-ctx.Done():
		t.Fatal("the old workspace-trust publication did not start")
	}

	retired := make(chan struct{})
	go func() {
		trust.Retire(services)
		close(retired)
	}()
	select {
	case <-sink.cancelStarted:
	case <-ctx.Done():
		t.Fatal("retirement did not start cancelling the old question")
	}
	replacementAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", []string{"replacement"})
		close(replacementAsked)
	}()
	awaitWorkspaceTrustReplacement(t, &trust, sink, requestID)

	otherAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-2", "/work/project", []string{"other session"})
		close(otherAsked)
	}()
	select {
	case <-otherAsked:
	case <-ctx.Done():
		t.Fatal("a blocked request ID blocked another session")
	}
	_, otherActive := sink.activeRequest(WorkspaceTrustRequestID("session-2"))
	assert.True(t, otherActive, "a blocked request ID must not block another session")
	releasePublish()
	select {
	case <-oldAsked:
	case <-ctx.Done():
		t.Fatal("the old workspace-trust publication did not finish")
	}
	assert.Equal(t, 1, sink.publishedCount(requestID), "the replacement must wait for the old publication")
	releaseCancel()
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("retirement did not finish")
	}
	select {
	case <-replacementAsked:
	case <-ctx.Done():
		t.Fatal("the replacement workspace-trust question did not finish")
	}

	var instances []string
	for _, request := range sink.PublishedControls() {
		if request.RequestID == requestID {
			instances = append(instances, request.InstanceID)
		}
	}
	require.Len(t, instances, 2)
	assert.NotEmpty(t, instances[0])
	assert.NotEmpty(t, instances[1])
	assert.NotEqual(t, instances[0], instances[1])
	active, ok := sink.activeRequest(requestID)
	assert.True(t, ok, "the replacement must remain published")
	if ok {
		assert.Equal(t, instances[1], active.InstanceID)
		assert.JSONEq(t, `{"type":"leapmux_workspace_trust","path":"/work/project","configuration":["replacement"]}`, string(active.Payload))
	}
	handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, requestID, contracts.WorkspaceTrustDecisionTrust))
	assert.True(t, handled)
	require.NoError(t, err, "the replacement must remain answerable")
	assert.Equal(t, 1, host.trustCalls)
	_, ok = sink.activeRequest(requestID)
	assert.False(t, ok, "answering the replacement retires its own instance")
}

// A second Retire invalidates a replacement while it waits for the old request
// ID to drain. It must not publish when the old operation finishes.
func TestWorkspaceTrustRetireWhileReplacementWaitsPreventsPublication(t *testing.T) {
	t.Parallel()
	sink := newWorkspaceTrustInstanceSink()
	services := agent.NewProviderServices(sink)
	releasePublish := sync.OnceFunc(func() { close(sink.releaseFirstPublish) })
	releaseCancel := sync.OnceFunc(func() { close(sink.releaseFirstCancel) })
	defer releasePublish()
	defer releaseCancel()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	requestID := WorkspaceTrustRequestID("session-1")
	ctx := testutil.DeadlineContext(t)
	oldAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", []string{"old"})
		close(oldAsked)
	}()
	select {
	case <-sink.publishStarted:
	case <-ctx.Done():
		t.Fatal("the old workspace-trust publication did not start")
	}

	retired := make(chan struct{})
	go func() {
		trust.Retire(services)
		close(retired)
	}()
	select {
	case <-sink.cancelStarted:
	case <-ctx.Done():
		t.Fatal("the first retirement did not start")
	}
	releaseCancel()
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("the first retirement did not finish")
	}
	replacementAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-1", "/work/project", []string{"replacement"})
		close(replacementAsked)
	}()
	awaitWorkspaceTrustReplacement(t, &trust, sink, requestID)
	trust.Retire(services)
	select {
	case <-replacementAsked:
	case <-ctx.Done():
		t.Fatal("the twice-retired replacement did not finish")
	}
	assert.Zero(t, sink.publishedCount(requestID), "a replacement retired while waiting must not publish")

	otherAsked := make(chan struct{})
	go func() {
		trust.Ask(services, host, "session-2", "/work/project", nil)
		close(otherAsked)
	}()
	select {
	case <-otherAsked:
	case <-ctx.Done():
		t.Fatal("a twice-retired request ID blocked another session")
	}
	_, otherActive := sink.activeRequest(WorkspaceTrustRequestID("session-2"))
	assert.True(t, otherActive)
	releasePublish()
	select {
	case <-oldAsked:
	case <-ctx.Done():
		t.Fatal("the old workspace-trust publication did not finish")
	}
	assert.Equal(t, 1, sink.publishedCount(requestID), "only the old question may publish")
	_, ok := sink.activeRequest(requestID)
	assert.False(t, ok)
	_, otherActive = sink.activeRequest(WorkspaceTrustRequestID("session-2"))
	assert.True(t, otherActive, "the old request ID must not affect the later session")
}

// An untrusted folder gets one question for each session, which states the folder
// and what the trust controls.
func TestWorkspaceTrustAsksOnceForEachSession(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", []string{"MCP servers"})
	require.Equal(t, 1, sink.PublishedControlCount())
	published := sink.LastPublishedControl()
	assert.Equal(t, WorkspaceTrustRequestID("session-1"), published.RequestID)
	assert.True(t, IsWorkspaceTrustRequestID(published.RequestID))
	assert.Equal(t, "session-1", published.AgentSessionID)
	assert.JSONEq(t, `{"type":"leapmux_workspace_trust","path":"/work/project","configuration":["MCP servers"]}`, string(published.Payload))

	trust.Ask(services, host, "session-1", "/work/project", []string{"MCP servers"})
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, 1, host.checks, "an open question needs no second check")

	trust.Ask(services, host, "session-2", "/work/project", []string{"MCP servers"})
	assert.Equal(t, 2, sink.PublishedControlCount(), "another session asks its own question")
}

// An agent that learns of the untrusted folder again, from an event that repeats,
// asks no second question once the reader answered the first.
func TestWorkspaceTrustAsksOnceForEachSessionAfterAnAnswer(t *testing.T) {
	t.Parallel()
	for _, decision := range []string{contracts.WorkspaceTrustDecisionTrust, contracts.WorkspaceTrustDecisionReject} {
		t.Run(decision, func(t *testing.T) {
			t.Parallel()
			sink, services := newWorkspaceTrustSink()
			host := &fakeWorkspaceTrustHost{}
			var trust WorkspaceTrust
			trust.Ask(services, host, "session-1", "/work/project", nil)
			handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, WorkspaceTrustRequestID("session-1"), decision))
			require.True(t, handled)
			require.NoError(t, err)

			trust.Ask(services, host, "session-1", "/work/project", nil)
			assert.Equal(t, 1, sink.PublishedControlCount())
			assert.Equal(t, 1, host.checks, "an answered session needs no second check")
		})
	}
}

// A question that Retire withdrew before an answer was never answered, so its
// session asks again when it comes back.
func TestWorkspaceTrustRetireLetsTheSessionAskAgain(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", nil)
	trust.Retire(services)
	trust.Ask(services, host, "session-1", "/work/project", nil)
	assert.Equal(t, 2, sink.PublishedControlCount())
	assert.Equal(t, 2, host.checks)
}

// A trusted folder and a failed check ask nothing.
func TestWorkspaceTrustAsksNothingWithoutAnUntrustedVerdict(t *testing.T) {
	t.Parallel()
	for name, host := range map[string]*fakeWorkspaceTrustHost{
		"a trusted folder": {trusted: true},
		"a failed check":   {checkErr: errors.New("no trust store")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink, services := newWorkspaceTrustSink()
			var trust WorkspaceTrust
			trust.Ask(services, host, "session-1", "/work/project", nil)
			assert.Zero(t, sink.PublishedControlCount())
		})
	}
}

// Trust lets the agent add the folder and retires the question.
func TestWorkspaceTrustTrustRetiresTheQuestion(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", nil)
	requestID := WorkspaceTrustRequestID("session-1")
	handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, requestID, contracts.WorkspaceTrustDecisionTrust))
	require.True(t, handled)
	require.NoError(t, err)
	assert.Equal(t, 1, host.trustCalls)
	assert.Equal(t, []string{requestID}, sink.CanceledControls())

	handled, err = trust.Answer(services, host, workspaceTrustAnswer(t, requestID, contracts.WorkspaceTrustDecisionTrust))
	assert.True(t, handled)
	require.ErrorIs(t, err, ErrWorkspaceTrustClosed, "an answered question takes no second answer")
	assert.Equal(t, 1, host.trustCalls)
}

// A failed trust and an unknown decision keep the question open for another answer.
func TestWorkspaceTrustKeepsTheQuestionAfterAFailedAnswer(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		trustErr error
		decision string
		wantText string
	}{
		{"a failed trust", errors.New("write failed"), contracts.WorkspaceTrustDecisionTrust, "write failed"},
		{"an unknown decision", nil, "maybe", "neither trust nor reject"},
		{"no decision", nil, "", "neither trust nor reject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			sink, services := newWorkspaceTrustSink()
			host := &fakeWorkspaceTrustHost{trustErr: test.trustErr}
			var trust WorkspaceTrust
			trust.Ask(services, host, "session-1", "/work/project", nil)
			requestID := WorkspaceTrustRequestID("session-1")
			handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, requestID, test.decision))
			require.True(t, handled)
			require.ErrorContains(t, err, test.wantText)
			assert.Empty(t, sink.CanceledControls())

			host.trustErr = nil
			handled, err = trust.Answer(services, host, workspaceTrustAnswer(t, requestID, contracts.WorkspaceTrustDecisionTrust))
			require.True(t, handled)
			require.NoError(t, err, "the question stays answerable")
			assert.Equal(t, []string{requestID}, sink.CanceledControls())
		})
	}
}

// Reject leaves the folder untrusted, retires the question, and the agent asks no
// more, in any session.
func TestWorkspaceTrustRejectAsksNoMore(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", nil)
	requestID := WorkspaceTrustRequestID("session-1")
	handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, requestID, contracts.WorkspaceTrustDecisionReject))
	require.True(t, handled)
	require.NoError(t, err)
	assert.Zero(t, host.trustCalls)
	assert.Equal(t, []string{requestID}, sink.CanceledControls())

	trust.Ask(services, host, "session-2", "/work/project", nil)
	assert.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, 1, host.checks, "a declined folder is not checked again")
}

// Retire withdraws the open question of a session that went away. Its answer is
// refused, and the next session asks again.
func TestWorkspaceTrustRetireWithdrawsTheOpenQuestion(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Retire(services)
	assert.Empty(t, sink.CanceledControls(), "nothing open, nothing withdrawn")
	trust.Ask(services, host, "session-1", "/work/project", nil)
	trust.Retire(services)
	assert.Equal(t, []string{WorkspaceTrustRequestID("session-1")}, sink.CanceledControls())
	handled, err := trust.Answer(services, host, workspaceTrustAnswer(t, WorkspaceTrustRequestID("session-1"), contracts.WorkspaceTrustDecisionTrust))
	assert.True(t, handled)
	require.ErrorIs(t, err, ErrWorkspaceTrustClosed)
	assert.Zero(t, host.trustCalls)

	trust.Ask(services, host, "session-2", "/work/project", nil)
	assert.Equal(t, 2, sink.PublishedControlCount())
}

// An answer to another request, and a frame that does not decode, are the
// provider's own to answer.
func TestWorkspaceTrustLeavesOtherAnswersAlone(t *testing.T) {
	t.Parallel()
	_, services := newWorkspaceTrustSink()
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	for _, raw := range [][]byte{
		workspaceTrustAnswer(t, "copilot-request", contracts.WorkspaceTrustDecisionTrust),
		[]byte(`not json`),
		[]byte(`{"jsonrpc":"2.0","id":7,"result":{}}`),
	} {
		handled, err := trust.Answer(services, host, raw)
		assert.False(t, handled, string(raw))
		assert.NoError(t, err, string(raw))
	}
	assert.Zero(t, host.trustCalls)
}

// A failed publication leaves no open question, so the next check asks again.
func TestWorkspaceTrustFailedPublicationAsksAgain(t *testing.T) {
	t.Parallel()
	sink, services := newWorkspaceTrustSink()
	sink.PublicationError = errors.New("the store is unavailable")
	host := &fakeWorkspaceTrustHost{}
	var trust WorkspaceTrust
	trust.Ask(services, host, "session-1", "/work/project", nil)
	sink.PublicationError = nil
	trust.Ask(services, host, "session-1", "/work/project", nil)
	assert.Equal(t, 2, host.checks)
	assert.Equal(t, 1, sink.PublishedControlCount())
}

// The browser's neutral decision becomes the saved answer {decision}. A stored
// request of another kind is the provider's own.
func TestResolveWorkspaceTrustResponse(t *testing.T) {
	t.Parallel()
	question := []byte(`{"type":"leapmux_workspace_trust","path":"/work/project","configuration":["MCP servers"]}`)
	requestID := WorkspaceTrustRequestID("session-1")
	for behavior, want := range map[string]string{
		agent.ControlBehaviorAllow: contracts.WorkspaceTrustDecisionTrust,
		agent.ControlBehaviorDeny:  contracts.WorkspaceTrustDecisionReject,
	} {
		content, err := json.Marshal(map[string]any{"response": map[string]any{"request_id": requestID, "response": map[string]string{"behavior": behavior}}})
		require.NoError(t, err)
		resolution, ok := ResolveWorkspaceTrustResponse(agent.ControlResponseContext{RequestID: requestID, RequestPayload: question, ResponseContent: content})
		require.True(t, ok, behavior)
		require.False(t, resolution.Withhold, behavior)
		assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+requestID+`","response":{"decision":"`+want+`"}}}`, string(resolution.Content), behavior)
	}

	for name, content := range map[string]string{
		"no decision":     `{"response":{"request_id":"` + requestID + `","response":{"behavior":"maybe"}}}`,
		"not an envelope": `not json`,
		"another request": `{"response":{"request_id":"workspace-trust:session-2","response":{"behavior":"allow"}}}`,
	} {
		resolution, ok := ResolveWorkspaceTrustResponse(agent.ControlResponseContext{RequestID: requestID, RequestPayload: question, ResponseContent: []byte(content)})
		require.True(t, ok, name)
		assert.True(t, resolution.Withhold, name)
	}

	_, ok := ResolveWorkspaceTrustResponse(agent.ControlResponseContext{RequestID: "other", RequestPayload: []byte(`{"type":"permission"}`), ResponseContent: []byte(`{}`)})
	assert.False(t, ok, "another kind of request")
}
