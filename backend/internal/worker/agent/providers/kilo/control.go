package kilo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// kiloQuestionControlID identifies one native question control request.
// The daemon's own question id is unique per daemon and already carries its `que_`
// prefix, so it needs no digest to separate it from another session's. The LeapMux
// prefix keeps it apart from the "jsonrpc:"-keyed rows of the stdio stream, which
// the same agent registers.
func kiloQuestionControlID(questionID string) string {
	return "opencode-question:" + questionID
}

// publishPending reconciles native questions with their control requests.
// PublishControlRequest keeps the claim token of a request it already carries, so a
// question that is on the reader's screen is announced again without disturbing it.
//
// Both halves are needed, and the second one was missing. A question answered in
// ANOTHER client while the event stream was down raises its `question.replied` on a
// connection LeapMux no longer holds, so nothing withdrew the card: the reader kept a
// live question card for a question the agent had moved past, and a click on one of
// its options reached a daemon that refuses it.
//
// A list this cannot read retires nothing. Every early return below leaves the cards
// as they are, because a failed read is not a statement that the daemon holds none.
func (q *kiloQuestions) publishPending(ctx context.Context) {
	// The client carries no timeout of its own, so every request states one. `consume`
	// calls this BEFORE it reads the event stream, so a daemon that accepts the
	// connection and then stalls on this route blocked the whole question bridge for
	// the life of the session, with nothing logged and no retry able to reach it.
	ctx, cancel := context.WithTimeout(ctx, kiloQuestionTimeout)
	defer cancel()
	questions, err := q.readPendingQuestions(ctx)
	if err != nil {
		slog.Debug("read pending Kilo questions", "agent_id", q.agentID, "error", err)
		return
	}
	held := make(map[string]struct{}, len(questions))
	for _, question := range questions {
		if requestID := q.publish(ctx, question); requestID != "" {
			held[requestID] = struct{}{}
		}
	}
	q.retireUnheld(held)
}

// retireUnheld cancels the card of every question the daemon no longer holds.
//
// The caller passes the request ids of the questions on the daemon's own list. This
// runs on the goroutine that reads the event stream, and that goroutine is the only
// one that publishes, so no question can enter `pending` between the list and this
// sweep. The answer path can still remove one, which leaves nothing to retire.
func (q *kiloQuestions) retireUnheld(held map[string]struct{}) {
	q.mu.Lock()
	var stale []string
	for requestID := range q.pending {
		if _, still := held[requestID]; still {
			continue
		}
		stale = append(stale, requestID)
	}
	for _, requestID := range stale {
		delete(q.pending, requestID)
	}
	q.mu.Unlock()
	for _, requestID := range stale {
		q.sink.CancelControlRequest(requestID)
	}
}

// publish raises ONE question as a control request.
//
// The stored payload is the event's own shape, `{type, properties}`, which is what
// the browser plugin reads. The event envelope's own id is left out on purpose: the
// shared response reader treats a stored request that carries a top-level `id` as a
// JSON-RPC request and withholds any answer whose id does not match it, and `evt_...`
// addresses the delivery rather than the question. The question's id is in
// `properties.id`, where this keys it from.
//
// It reports the request id it registered, or "" for a frame it could not publish.
// publishPending collects those ids to decide which cards the daemon no longer
// holds, and a frame this could not read must not retire the card it identifies.
func (q *kiloQuestions) publish(ctx context.Context, properties json.RawMessage) string {
	var question struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(properties, &question); err != nil || question.ID == "" {
		slog.Warn("Read an Kilo question", "agent_id", q.agentID, "error", err)
		return ""
	}
	payload, err := json.Marshal(struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}{Type: contracts.NativeQuestionEventQuestionAsked, Properties: properties})
	if err != nil {
		slog.Error("Build an Kilo question payload", "agent_id", q.agentID, "error", err)
		return ""
	}
	requestID := kiloQuestionControlID(question.ID)
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[string]string)
	}
	q.pending[requestID] = question.ID
	q.mu.Unlock()
	if err := q.sink.PublishControlRequest(agent.ControlRequest{
		RequestID:      requestID,
		Payload:        payload,
		AgentSessionID: question.SessionID,
	}); err != nil {
		slog.Error("Publish an Kilo question", "agent_id", q.agentID, "request_id", requestID, "error", err)
		q.forget(requestID)
		// The daemon blocks on this question. A reader who will never see it must not
		// leave the turn waiting, so the question is rejected here and the agent
		// carries on with the refusal.
		if err := q.reject(ctx, question.ID); err != nil {
			slog.Warn("Reject an Kilo question LeapMux could not publish",
				"agent_id", q.agentID, "request_id", requestID, "error", err)
		}
		// The record is gone, so the caller must not count this question as held.
		return ""
	}
	return requestID
}

// withdraw retires the card of a question the daemon reports as settled.
func (q *kiloQuestions) withdraw(properties json.RawMessage) {
	var settled struct {
		RequestID string `json:"requestID"`
	}
	if err := json.Unmarshal(properties, &settled); err != nil || settled.RequestID == "" {
		return
	}
	requestID := kiloQuestionControlID(settled.RequestID)
	if q.forget(requestID) {
		q.sink.CancelControlRequest(requestID)
	}
}

// forget drops one record and reports whether it was there. The answer path and the
// event path both retire a question, and only the one that took the record acts.
func (q *kiloQuestions) forget(requestID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, existed := q.pending[requestID]
	delete(q.pending, requestID)
	return existed
}

func (q *kiloQuestions) cancelAll() {
	q.mu.Lock()
	requestIDs := make([]string, 0, len(q.pending))
	for requestID := range q.pending {
		requestIDs = append(requestIDs, requestID)
	}
	clear(q.pending)
	q.mu.Unlock()
	for _, requestID := range requestIDs {
		q.sink.CancelControlRequest(requestID)
	}
}

// answer sends the reader's decision to the daemon, and reports whether the frame
// was one of its questions.
//
// A frame it does not claim goes to the stdio stream unchanged, which is where every
// other control answer belongs.
func (q *kiloQuestions) answer(ctx context.Context, raw []byte) (bool, error) {
	if q.client == nil || q.base() == "" {
		return false, nil
	}
	_, requestID, ok := agent.ExtractJSONRPCID(raw)
	if !ok || requestID == "" {
		return false, nil
	}
	q.mu.Lock()
	questionID, pending := q.pending[requestID]
	q.mu.Unlock()
	if !pending {
		return false, nil
	}
	var frame struct {
		Result kiloAnswerResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return true, fmt.Errorf("decode the Kilo question answer: %w", err)
	}
	var err error
	switch {
	case frame.Result.Rejected:
		err = q.reject(ctx, questionID)
	case frame.Result.Answers != nil:
		err = q.reply(ctx, questionID, frame.Result.Answers)
	default:
		return true, fmt.Errorf("the Kilo question answer carries neither answers nor a rejection")
	}
	if err != nil {
		return true, err
	}
	// The daemon states `question.replied` as well, and that arrives on the event
	// stream. Whichever reaches the record first retires it; the other finds nothing.
	q.forget(requestID)
	return true, nil
}

// kiloAnswerResult is the answer envelope the BROWSER writes and this reads back.
//
// A Go struct tag cannot hold a constant, so the two field names are literals here
// and TestKiloAnswerTagsMatchTheContract pins them to
// contracts/native-questions.json, which the browser reads the same names from. A
// rename on one side alone used to leave `Answers` nil, so `reply` never ran and the
// daemon's question tool blocked for the rest of the session with nothing logged.
type kiloAnswerResult struct {
	Answers  [][]string `json:"answers"`
	Rejected bool       `json:"rejected"`
}

func (q *kiloQuestions) reply(ctx context.Context, questionID string, answers [][]string) error {
	return q.post(ctx, questionID, "reply", struct {
		Answers [][]string `json:"answers"`
	}{Answers: answers})
}

func (q *kiloQuestions) reject(ctx context.Context, questionID string) error {
	return q.post(ctx, questionID, "reject", nil)
}
