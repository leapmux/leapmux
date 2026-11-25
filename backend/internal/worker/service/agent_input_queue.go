package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

type agentInputQueueAdapter struct {
	svc *Service
}

// queuedInputTextPreview caps the text one snapshot item puts on the wire.
// The store already truncates in SQL, so this never fires for a snapshot the
// store built. It is the guard at the wire boundary itself: a snapshot holds
// up to MaxItems entries, and the channel drops a message over
// contracts.MaxMessageSize, so an untruncated item would disconnect every
// watcher instead of showing a long message. The limit is the store's own
// preview limit measured in bytes, so the two layers cannot state different
// numbers.
func queuedInputTextPreview(value string) string {
	if len(value) <= inputqueue.SnapshotTextPreviewBytes {
		return value
	}
	end := inputqueue.SnapshotTextPreviewBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}

// parseQueueSchedule reads one external schedule field. An empty value means
// immediate; anything else parses as RFC3339, normalizes to UTC, and floors to
// the millisecond so the wire instant survives the canonical DATETIME column
// byte-exactly. A malformed value is ErrInvalidInput, and a past value stays
// valid queue data: the queue counts it immediate. The normalized UTC instant
// must still fit the RFC3339 year range.
func parseQueueSchedule(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%w: scheduled_at is not an RFC3339 timestamp", inputqueue.ErrInvalidInput)
	}
	at = sqltime.FloorMillis(at.UTC())
	if _, err := at.MarshalText(); err != nil {
		return nil, fmt.Errorf("%w: scheduled_at is outside the UTC RFC3339 range", inputqueue.ErrInvalidInput)
	}
	return &at, nil
}

// queueAttachments copies the attachments of a request into the queue's form.
//
// Each attachment takes the media type that the classifier gives it, which is
// the type that the provider receives at dispatch (NormalizeAttachments reads
// the same classifier): a stated type in lower case, and for a type that is
// absent or the generic application/octet-stream, the one that the classifier
// infers from the name and the content. The queue's snapshot and the
// transcript row then state that type too, so no client sees a type that the
// provider does not read. The browser infers the type before it sends, and
// the CLI leaves it to the Worker, so both clients store one answer. The
// classification is a function of the request, so a repeated request still
// matches the input that it repeats.
func queueAttachments(attachments []*leapmuxv1.Attachment) []inputqueue.Attachment {
	result := make([]inputqueue.Attachment, len(attachments))
	for i, attachment := range attachments {
		mimeType := attachment.GetMimeType()
		if classified := agent.ClassifyAttachments([]*leapmuxv1.Attachment{attachment}); len(classified) == 1 {
			mimeType = classified[0].MIMEType
		}
		result[i] = inputqueue.Attachment{
			Filename: attachment.GetFilename(),
			MimeType: mimeType,
			Data:     append([]byte(nil), attachment.GetData()...),
		}
	}
	return result
}

// refuseUnreadableAttachments applies the attachment policy of the agent's
// provider to the attachments of a client's enqueue or edit. A file that the
// provider cannot read then fails the request that sent it, and the refusal
// states the file. The dispatch applies the same policy, through the same
// NormalizeAttachments, but a refusal there fails the input in the queue, and
// a failed first input holds every input behind it. The browser checks the
// provider's capabilities before it sends, and the CLI cannot, because it
// holds no provider code.
//
// A missing agent passes, because the queue refuses the request for it with
// its own error.
func (svc *Service) refuseUnreadableAttachments(agentID string, attachments []*leapmuxv1.Attachment) error {
	if len(attachments) == 0 {
		return nil
	}
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := svc.Agents.Registry().NormalizeAttachments(dbAgent.AgentProvider, attachments); err != nil {
		return fmt.Errorf("%w: %w", inputqueue.ErrInvalidInput, err)
	}
	return nil
}

func providerAttachments(attachments []inputqueue.Attachment) []*leapmuxv1.Attachment {
	result := make([]*leapmuxv1.Attachment, len(attachments))
	for i := range attachments {
		result[i] = &leapmuxv1.Attachment{
			Filename: attachments[i].Filename,
			MimeType: attachments[i].MimeType,
			Data:     append([]byte(nil), attachments[i].Data...),
		}
	}
	return result
}

// queueReadError classifies a read of the worker's own store that a dispatch or a
// steer made before it reached the provider.
//
// A store fault requeues and pauses; a missing row stays a permanent failure, because
// the agent it identifies is gone.
//
// Prefer queueAgentRow to calling this by hand. The rule belongs at the READ, not at
// each call site: applied by hand it covered four of the nine store reads on these two
// paths, and three of the five it missed failed the reader's typed message PERMANENTLY
// for a database error that never reached the provider.
func queueReadError(err error) error {
	if storeFault(err) {
		return notReadyStoreFault(err)
	}
	return err
}

// queueAgentRow reads the agent row for a dispatch or a steer, already classified.
//
// Every read on those two paths goes through here, so a read added later inherits the
// classification instead of having to remember it.
func (svc *Service) queueAgentRow(agentID string) (db.Agent, error) {
	dbAgent, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return db.Agent{}, queueReadError(err)
	}
	return dbAgent, nil
}

// queueChildRouteRow reads a subagent's background-task row for a dispatch or a steer,
// already classified. It is queueAgentRow for the other row these two paths read.
func (svc *Service) queueChildRouteRow(childAgentID string) (db.AgentBackgroundTask, error) {
	row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(bgCtx(), childAgentID)
	if err != nil {
		return db.AgentBackgroundTask{}, queueReadError(err)
	}
	return row, nil
}

func (a *agentInputQueueAdapter) Dispatch(item inputqueue.DispatchItem) (inputqueue.DispatchResult, error) {
	svc := a.svc
	spanLines := svc.Output.snapshotPassthroughSpanLines(item.AgentID)
	dbAgent, err := svc.queueAgentRow(item.AgentID)
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	resolvedInput, err := svc.resolveControlInput(item, dbAgent)
	if err != nil {
		return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
	}
	text := resolvedInput.text
	// An agent whose last startup failed takes no input, except an explicit
	// /clear: the clear starts a fresh session and recovers the agent (see
	// clearRecovery), and the refusal of a native resume tells the user to send
	// it. Every other kind reaches ensureAgentRunning, which refuses the agent
	// too.
	//
	// This check runs before the in-flight join in ensureAgentRunning, so it
	// cannot see a startup that fails during that join. ensureAgentRunning
	// asks the same predicate again after the join.
	if item.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT && svc.agentStartupFailed(&dbAgent) {
		return inputqueue.DispatchResult{}, errAgentStartupFailed
	}
	attachments, err := a.svc.Agents.Registry().NormalizeAttachments(dbAgent.AgentProvider, providerAttachments(item.Attachments))
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}

	if dbAgent.ParentAgentID.Valid {
		if !childInputKind(item.Kind) {
			return inputqueue.DispatchResult{}, fmt.Errorf("subagent accepts only messages")
		}
		row, err := svc.queueChildRouteRow(item.AgentID)
		if err != nil {
			return inputqueue.DispatchResult{}, err
		}
		if !svc.Agents.AgentAlive(row.OwnerAgentID) {
			return inputqueue.DispatchResult{}, &inputqueue.DeliveryError{Err: agent.ErrAgentNotFound, Outcome: inputqueue.DispatchNotReady}
		}
		result, err := svc.Agents.SendChildInput(row.OwnerAgentID, row.RowKey, agent.ChildInput{InputID: item.ID, Content: text, Attachments: attachments, Ready: svc.childInputReady(item.AgentID)})
		if err != nil {
			if errors.Is(err, agent.ErrChildOperationUnsupported) {
				return inputqueue.DispatchResult{}, inputqueue.ErrSteeringUnsupported
			}
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{
			StartsTurn: result.StartsTurn, TurnSteerable: a.SupportsSteering(item.AgentID), SpanLines: spanLines, NativeReceipt: result.NativeReceipt,
		}, nil
	}

	ensureRunning := func() error {
		if !svc.Agents.AgentAlive(item.AgentID) && svc.Agents.HasAgent(item.AgentID) {
			// The process exited and its exit callback still runs. The slot
			// stays registered until that callback pauses the queue, and
			// ensureAgentRunning returns at once for a registered slot, so a
			// start here would resolve the dying provider and write to a
			// closed pipe. Return the input to the queue instead.
			return &inputqueue.DeliveryError{Err: agent.ErrAgentNotFound, Outcome: inputqueue.DispatchNotReady}
		}
		// Readiness also checks a running process. A process that requires
		// recovery holds the input for approval before native delivery.
		// ensureAgentRunning resolves the resumed session itself, from the row
		// that it reads after the join. dbAgent was read before the join and
		// before a plan context replaced the session, so a session resolved
		// from it can be one that the provider already moved away from.
		return svc.ensureAgentReadyForInput(item.AgentID)
	}

	switch item.Kind {
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT:
		// A clear STOPS the process, and it is the one dispatch that destroys a
		// turn instead of joining it. The queue's own active_turn guard already
		// holds it behind a running turn, but that guard is a durable COPY of
		// the provider's flag, and the store's own doc records the cases where
		// the copy can be stale. Ask the flag itself before the stop: a wrong
		// copy then costs a requeue on an open queue rather than the user's
		// in-flight reply, killed mid-sentence with no result.
		if svc.Output.TurnActive(item.AgentID) {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(agent.ErrAgentBusy)
		}
		afterAccept, err := svc.prepareClearContext(item.AgentID)
		if err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: false, SpanLines: spanLines, AfterAccept: afterAccept}, nil
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT:
		if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		err := svc.Agents.CompactContext(item.AgentID)
		if errors.Is(err, agent.ErrCompactionUnsupported) {
			err = svc.Agents.SendInput(item.AgentID, text, attachments)
		}
		if err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, TurnSteerable: a.SupportsSteering(item.AgentID), SpanLines: spanLines}, nil
	case leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION:
		if item.PrepareContext {
			if err := svc.prepareApprovedPlanContext(item, &resolvedInput); err != nil {
				return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
			}
		}
		if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		if item.PrepareContext {
			if err := svc.confirmPreparedPlanSettings(item, resolvedInput); err != nil {
				return inputqueue.DispatchResult{}, err
			}
		}
		if _, err := svc.sendResolvedInput(item.AgentID, resolvedInput, attachments); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{StartsTurn: true, TurnSteerable: a.SupportsSteering(item.AgentID), SpanLines: svc.Output.snapshotPassthroughSpanLines(item.AgentID)}, nil
	default:
		if err := ensureRunning(); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		receipt, err := svc.sendResolvedInput(item.AgentID, resolvedInput, attachments)
		if err != nil {
			return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
		}
		return inputqueue.DispatchResult{
			StartsTurn: true, TurnSteerable: a.SupportsSteering(item.AgentID), SpanLines: spanLines,
			SupplementalContent: resolvedInput.supplementalContent,
			NativeReceipt:       receipt,
		}, nil
	}
}

// classifyQueueDeliveryError turns a provider error into the queue's own
// outcome. Transient refusals must not become a permanent
// failure that the user has to retry by hand. These mean the input never
// reached the provider, so redispatch is safe.
//
// Two of them need the agent to change state first, so the queue pauses and
// waits for the cause that changes it (DispatchNotReady):
//
//   - ErrChildRouteNotReady: the owner process runs, but it did not rebuild
//     the child route yet. Every Worker restart produces this condition.
//   - ErrAgentNotFound: the process exited between the readiness test and the
//     write. Manager returns it from providerAfterLifecycle, which resolves the
//     provider BEFORE any write, so nothing reached the agent.
//
// A busy turn releases its waiting input when it ends (DispatchBusy):
//
//   - ErrAgentBusy: the provider is already inside a turn that the queue's own
//     active_turn view did not record. The agent works, and the end of that
//     turn releases the item on its own -- so a pause here would stop a queue
//     that has no problem, and hold it stopped until the user resumed it by
//     hand.
//
// ErrInputPending waits for an earlier input's native admission. Its readiness
// notification releases the queue without claiming that a turn ran.
func classifyQueueDeliveryError(err error) error {
	var stated *inputqueue.DeliveryError
	if errors.As(err, &stated) {
		// The refusal already carries its outcome. A second wrap would discard
		// it, because queueDispatchOutcome reads the CAUSE and answers
		// DispatchFailed for every cause it does not know.
		return err
	}
	if !errors.Is(err, agent.ErrDeliveryUncertain) && errors.Is(err, agent.ErrInputProcessRestartRequired) {
		return processRecoveryRequired(err)
	}
	deliveryErr := &inputqueue.DeliveryError{Err: err, Outcome: queueDispatchOutcome(err)}
	var busyErr *agent.AgentBusyError
	if errors.As(err, &busyErr) {
		deliveryErr.ActiveTurnSteerable = busyErr.ActiveTurnSteerable
	}
	return deliveryErr
}

func queueDispatchOutcome(err error) inputqueue.DispatchOutcome {
	switch {
	case errors.Is(err, agent.ErrInputPending):
		return inputqueue.DispatchPending
	case errors.Is(err, agent.ErrAgentBusy):
		return inputqueue.DispatchBusy
	case errors.Is(err, agent.ErrChildRouteNotReady), errors.Is(err, agent.ErrAgentNotFound), errors.Is(err, errWorkerShuttingDown):
		return inputqueue.DispatchNotReady
	case errors.Is(err, agent.ErrDeliveryUncertain):
		return inputqueue.DispatchUncertain
	case errors.Is(err, agent.ErrInputProcessRestartRequired):
		return inputqueue.DispatchNotReady
	default:
		return inputqueue.DispatchFailed
	}
}

func classifyQueueSteerError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, agent.ErrNoActiveTurn):
		return inputqueue.ErrTurnEnded
	case errors.Is(err, agent.ErrSteeringUnsupported), errors.Is(err, agent.ErrChildOperationUnsupported):
		return inputqueue.ErrSteeringUnsupported
	default:
		return classifyQueueDeliveryError(err)
	}
}

func (a *agentInputQueueAdapter) Steer(item inputqueue.DispatchItem) (inputqueue.DispatchResult, error) {
	dbAgent, err := a.svc.queueAgentRow(item.AgentID)
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	resolvedInput, err := a.svc.resolveControlInput(item, dbAgent)
	if err != nil {
		return inputqueue.DispatchResult{}, classifyQueueDeliveryError(err)
	}
	attachments, err := a.svc.Agents.Registry().NormalizeAttachments(dbAgent.AgentProvider, providerAttachments(item.Attachments))
	if err != nil {
		return inputqueue.DispatchResult{}, err
	}
	// The request states the item, so a provider that drops the steer can hand
	// it back (see Service.requeueDroppedInput).
	request := agent.SteerRequest{InputID: item.ID, AttemptID: item.SteerAttemptID, Content: resolvedInput.text, Attachments: attachments}
	var receipt agent.NativeInputReceipt
	if dbAgent.ParentAgentID.Valid {
		row, rowErr := a.svc.queueChildRouteRow(item.AgentID)
		if rowErr != nil {
			return inputqueue.DispatchResult{}, rowErr
		}
		receipt, err = a.svc.Agents.SteerChildInput(row.OwnerAgentID, row.RowKey, request)
	} else if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
		// A validated answer steers into an existing turn, so it must keep
		// reaching the process whose question asked it, even while new prompts
		// wait for recovery approval. An
		// idle question ends its steer and returns through Dispatch readiness.
		err = a.svc.steerResolvedInput(item.AgentID, resolvedInput, request)
	} else {
		// A new prompt steered at the root is subject to the same readiness as
		// a dispatched one: a process that requires recovery holds the prompt
		// for approval before the steer reaches it.
		if err := a.svc.ensureAgentReadyForInput(item.AgentID); err != nil {
			return inputqueue.DispatchResult{}, classifyQueueSteerError(err)
		}
		receipt, err = a.svc.Agents.SteerInput(item.AgentID, request)
	}
	return inputqueue.DispatchResult{
		StartsTurn:          true,
		SpanLines:           a.svc.Output.snapshotPassthroughSpanLines(item.AgentID),
		SupplementalContent: resolvedInput.supplementalContent,
		NativeReceipt:       receipt,
	}, classifyQueueSteerError(err)
}

// AcceptsKind reports whether this agent can receive the input kind.
// A child receives user messages only when its provider supports that route.
// Native control feedback stays independent of user message support.
// A child cannot execute root slash commands such as clear or compact.
// Enqueue refuses unsupported input before it changes the durable queue.
func (a *agentInputQueueAdapter) AcceptsKind(agentID string, kind leapmuxv1.AgentInputKind) bool {
	dbAgent, err := a.svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		// The enqueue itself fails on the missing agent, so admit the kind and
		// let that error carry the reason.
		return true
	}
	if !dbAgent.ParentAgentID.Valid {
		return true
	}
	if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE || kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE {
		return a.svc.Agents.Registry().Plugin(dbAgent.AgentProvider).ChildCapabilities(parseOptions(dbAgent.Options)).AcceptsMessages
	}
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK
}

// childInputKind selects the kinds that a child agent's route delivers: a
// message, from the user or from another agent, and control feedback.
func childInputKind(kind leapmuxv1.AgentInputKind) bool {
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AGENT_MESSAGE ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK
}

func (a *agentInputQueueAdapter) PrepareSteer(agentID string) {
	a.svc.Agents.PrepareSteer(agentID)
}

func (a *agentInputQueueAdapter) SteerPolicy(agentID string) (agent.SteerPolicy, error) {
	if a.svc.Agents.HasAgent(agentID) {
		return a.svc.Agents.SteerPolicy(agentID), nil
	}
	row, err := a.svc.queueChildRouteRow(agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.SteerPolicy{}, nil
	}
	if err != nil {
		return agent.SteerPolicy{}, err
	}
	return a.svc.Agents.ChildSteerPolicy(row.OwnerAgentID), nil
}

func (a *agentInputQueueAdapter) SupportsSteering(agentID string) bool {
	dbAgent, err := a.svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return false
	}
	return a.svc.agentSupportsSteering(&dbAgent)
}

// Interrupt cancels the agent's active turn for Preempt. It is the provider
// call alone: the queue pause, stop notices and control withdrawal that the
// InterruptAgent RPC applies belong to a user STOP, and a stop-pause would hold
// the very dispatch Preempt exists to unblock.
func (a *agentInputQueueAdapter) Interrupt(agentID string) error {
	err := a.svc.Agents.Interrupt(agentID, agent.StopContext{})
	if errors.Is(err, agent.ErrAgentNotFound) {
		return fmt.Errorf("agent is not running: %w", err)
	}
	return err
}

// SupportsPreemption answers whether Preempt may be offered. It fetches the row
// and delegates, exactly as SupportsSteering above does, so the rule itself
// lives in one place: see agentSupportsPreemption.
func (a *agentInputQueueAdapter) SupportsPreemption(agentID string) bool {
	dbAgent, err := a.svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return false
	}
	return a.svc.agentSupportsPreemption(&dbAgent)
}

// agentSupportsSteering answers the capability from a row the caller already
// holds. agentToProto, buildAgentActiveStatus and the output sink's
// buildStatusChange each hold one, and ListAgents runs agentToProto for every
// requested tab, so an ID-based call would repeat a `SELECT *` on the agents
// row once per agent.
func (svc *Service) agentSupportsSteering(dbAgent *db.Agent) bool {
	if dbAgent.ParentAgentID.Valid {
		if !svc.Agents.Registry().Plugin(dbAgent.AgentProvider).ChildCapabilities(parseOptions(dbAgent.Options)).AcceptsMessages {
			return false
		}
		row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(bgCtx(), dbAgent.ID)
		return err == nil && svc.Agents.SupportsSteering(row.OwnerAgentID)
	}
	return svc.Agents.SupportsSteering(dbAgent.ID)
}

// agentSupportsPreemption answers preemption's capability from a row the
// caller already holds: a running ROOT provider that cannot steer. Steering
// keeps its own control for the providers that have it; preemption is what
// the rest (Cursor, Kilo) get. Children are excluded until a provider pairing
// is verified against the child-interrupt route. The HasAgent term keeps a
// not-running provider from answering true through the negated steering
// capability alone.
//
// This is the ONE statement of the rule. The queue adapter's SupportsPreemption
// fetches the row and calls it, rather than re-spelling the three terms.
//
// It tests the STEERING half alone, and the interrupt half needs no test: Interrupt
// is a required method of the Agent interface, so a provider with no interrupt path
// does not compile. Process carried a no-op default until this was written, and
// a provider that inherited it would have answered the RPC with success, cancelled
// nothing, and left the reader a Preempt button that did nothing.
func (svc *Service) agentSupportsPreemption(dbAgent *db.Agent) bool {
	return !dbAgent.ParentAgentID.Valid && svc.Agents.HasAgent(dbAgent.ID) && !svc.agentSupportsSteering(dbAgent)
}

// queueHeadDispatch is the ONE statement of how a queued head reaches the
// agent while its turn runs: STEER when the running provider can steer, else
// PREEMPT when it can be preempted, else NONE. An agent that does not run
// answers NONE. AgentInfo and every full AgentStatusChange carry this value,
// so the client reads one field whose three states exclude each other. It
// never answers UNSPECIFIED, which a partial status change leaves for "no
// update".
func (svc *Service) queueHeadDispatch(dbAgent *db.Agent, running bool) leapmuxv1.QueueHeadDispatch {
	switch {
	case !running:
		return leapmuxv1.QueueHeadDispatch_QUEUE_HEAD_DISPATCH_NONE
	case svc.agentSupportsSteering(dbAgent):
		return leapmuxv1.QueueHeadDispatch_QUEUE_HEAD_DISPATCH_STEER
	case svc.agentSupportsPreemption(dbAgent):
		return leapmuxv1.QueueHeadDispatch_QUEUE_HEAD_DISPATCH_PREEMPT
	default:
		return leapmuxv1.QueueHeadDispatch_QUEUE_HEAD_DISPATCH_NONE
	}
}

func (a *agentInputQueueAdapter) QueueChanged(snapshot inputqueue.Snapshot) {
	a.svc.Watchers.EnqueueAgentEvent(snapshot.AgentID, &leapmuxv1.AgentEvent{
		AgentId: snapshot.AgentID,
		Event: &leapmuxv1.AgentEvent_InputQueueChanged{
			InputQueueChanged: &leapmuxv1.AgentInputQueueChanged{Snapshot: a.svc.queueSnapshotProto(snapshot)},
		},
	})
	a.svc.Watchers.EnqueueAgentEvent(snapshot.AgentID, &leapmuxv1.AgentEvent{
		AgentId: snapshot.AgentID,
		Event:   &leapmuxv1.AgentEvent_InputQueueRevision{InputQueueRevision: snapshot.Revision},
	})
	a.svc.Watchers.DrainAgentEvents(snapshot.AgentID)
}

func (a *agentInputQueueAdapter) InputAccepted(message inputqueue.AcceptedTranscript) {
	a.svc.Watchers.BroadcastAgentEvent(message.AgentID, &leapmuxv1.AgentEvent{
		AgentId: message.AgentID,
		Event:   &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: acceptedTranscriptProto(message)},
	})
}

// acceptedTranscriptProto builds the live row for an input the provider took.
// It must carry every column that a later read of the same database row
// returns, or the bubble renders one way now and another way after a reload.
func acceptedTranscriptProto(message inputqueue.AcceptedTranscript) *leapmuxv1.AgentChatMessage {
	return &leapmuxv1.AgentChatMessage{
		Id: message.ID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: message.Content, ContentCompression: message.ContentCompression,
		SupplementalContent: message.SupplementalContent, SupplementalContentCompression: message.SupplementalContentCompression,
		Seq: message.Seq, AgentProvider: message.AgentProvider, AgentSessionId: message.AgentSessionID,
		MarkType: message.MarkType, SpanLines: message.SpanLines,
		CreatedAt: message.CreatedAt,
	}
}

// queueScheduleProto emits the canonical UTC timestamp the Worker derived, or
// empty for an immediate item.
func queueScheduleProto(scheduledAt *time.Time) string {
	if scheduledAt == nil {
		return ""
	}
	return timefmt.Format(*scheduledAt)
}

func queueSnapshotProto(snapshot inputqueue.Snapshot) *leapmuxv1.AgentInputQueueSnapshot {
	// PauseReason alone carries the pause: the store pauses exactly when it
	// holds a reason, and a stored reason is never UNSPECIFIED.
	result := &leapmuxv1.AgentInputQueueSnapshot{
		Revision: snapshot.Revision, PauseReason: snapshot.PauseReason,
		ActiveTurn: snapshot.ActiveTurn, ActiveTurnSteerable: snapshot.ActiveTurnSteerable,
		Items: make([]*leapmuxv1.QueuedAgentInput, len(snapshot.Items)),
	}
	for i := range snapshot.Items {
		item := snapshot.Items[i]
		attachments := make([]*leapmuxv1.QueuedAgentInputAttachment, len(item.Metadata))
		for j := range item.Metadata {
			attachments[j] = &leapmuxv1.QueuedAgentInputAttachment{
				Filename: item.Metadata[j].Filename, MimeType: item.Metadata[j].MimeType,
				Size: item.Metadata[j].Size, Order: item.Metadata[j].Order,
			}
		}
		result.Items[i] = &leapmuxv1.QueuedAgentInput{
			Id: item.ID, Kind: item.Kind, Sender: item.Sender.Proto(), Text: queuedInputTextPreview(item.TextPreview),
			Attachments: attachments, Order: item.Position, State: item.State,
			Error: item.Error, EditOwnerClientId: item.EditOwner, Version: item.Version,
			CanSteer: item.CanSteer, CanPreempt: item.CanPreempt, EditRefusal: item.EditRefusal,
			ScheduledAt: queueScheduleProto(item.ScheduledAt), ReorderSegment: item.ReorderSegment,
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		}
	}
	return result
}

func registerAgentInputQueueHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerAgentInputRecoveryHandlers(d, svc)
	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, dispatchPlain,
		func(_ context.Context, caller channel.Caller, r *leapmuxv1.EnqueueAgentInputRequest, sender channel.ResponseWriter) {
			if r.GetKind() == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
				sendQueueError(sender, fmt.Errorf("%w: the worker creates question-answer inputs", inputqueue.ErrInvalidInput))
				return
			}
			scheduledAt, err := parseQueueSchedule(r.GetScheduledAt())
			if err != nil {
				sendQueueError(sender, err)
				return
			}
			// The caller's tab, never the request, says which agent sent the
			// input. The queue turns a user message from an agent into an
			// agent message (see inputqueue.Store.admitNewItem).
			from, err := svc.agentMessageSender(caller)
			if sendQueueError(sender, err) {
				return
			}
			if sendQueueError(sender, svc.refuseUnreadableAttachments(r.GetAgentId(), r.GetAttachments())) {
				return
			}
			snapshot, receipt, err := svc.InputQueue.EnqueueWithReceipt(bgCtx(), inputqueue.NewItem{
				ID: r.GetInputId(), AgentID: r.GetAgentId(), Kind: r.GetKind(), Sender: from, Text: r.GetText(),
				Attachments:           queueAttachments(r.GetAttachments()),
				ReclassifyOnEdit:      r.GetKind() == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				ScheduledAt:           scheduledAt,
				RequireFutureSchedule: r.GetRequireFutureSchedule(),
				RequireExisting:       r.GetRequireExisting(),
			})
			if errors.Is(err, inputqueue.ErrInputNotRecorded) {
				sendProtoResponse(sender, &leapmuxv1.EnqueueAgentInputResponse{ExistingInputMissing: true})
				return
			}
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.EnqueueAgentInputResponse{
				Snapshot: svc.queueSnapshotProto(snapshot), AcceptedSeq: receipt.AcceptedSeq,
			})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.ListAgentInputQueueRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Snapshot(ctx, r.GetAgentId())
			if sendQueueError(sender, err) {
				return
			}
			response := &leapmuxv1.ListAgentInputQueueResponse{Snapshot: svc.queueSnapshotProto(snapshot)}
			if inputID := r.GetReplyInputId(); inputID != "" {
				state, err := svc.Queries.GetInputReplyState(ctx, db.GetInputReplyStateParams{
					AgentID: r.GetAgentId(), InputID: inputID, UserSource: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
				})
				switch {
				case err == nil:
					outcome := leapmuxv1.AgentInputTurnOutcome(state.Outcome)
					canonical, err := svc.Queries.GetCanonicalInputReplyOutcome(ctx, db.GetCanonicalInputReplyOutcomeParams{
						AgentID: r.GetAgentId(), InputID: inputID, UserSource: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
					})
					switch {
					case err == nil:
						outcome = canonical.Outcome
						if canonical.MissingItems {
							outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED
						}
					case !errors.Is(err, sql.ErrNoRows):
						sendInternalError(sender, "failed to read the canonical input reply")
						return
					}
					response.InputReply = &leapmuxv1.AgentInputTurnReply{
						AcceptedSeq: state.AcceptedSeq, Outcome: outcome, WaitingForUser: outcome == 0 && svc.inputWaitingForUser(r.GetAgentId(), state),
					}
				case !errors.Is(err, sql.ErrNoRows):
					sendQueueError(sender, err)
					return
				}
			}
			sendProtoResponse(sender, response)
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceGetQueuedAgentInputProcedure, dispatchPlain,
		func(ctx context.Context, _ channel.Caller, r *leapmuxv1.GetQueuedAgentInputRequest, sender channel.ResponseWriter) {
			if strings.TrimSpace(r.GetInputId()) == "" {
				sendInvalidArgument(sender, "input_id is required")
				return
			}
			snapshot, fullText, err := svc.InputQueue.GetItem(ctx, r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			protoSnap := svc.queueSnapshotProto(snapshot)
			var matched *leapmuxv1.QueuedAgentInput
			for _, it := range protoSnap.GetItems() {
				if it.GetId() == r.GetInputId() {
					matched = it
					break
				}
			}
			if matched == nil {
				sendQueueError(sender, inputqueue.ErrNotFound)
				return
			}
			sendProtoResponse(sender, &leapmuxv1.GetQueuedAgentInputResponse{
				Item: matched,
				Text: fullText,
			})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.BeginQueuedAgentInputEditRequest, sender channel.ResponseWriter) {
			snapshot, fullText, attachments, err := svc.InputQueue.BeginEdit(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetClientId(), r.GetTakeover())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.BeginQueuedAgentInputEditResponse{
				Snapshot: svc.queueSnapshotProto(snapshot), Attachments: providerAttachments(attachments), Text: fullText,
			})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.UpdateQueuedAgentInputRequest, sender channel.ResponseWriter) {
			schedule := inputqueue.KeepSchedule()
			if r.ScheduledAt != nil {
				scheduledAt, err := parseQueueSchedule(r.GetScheduledAt())
				if err != nil {
					sendQueueError(sender, err)
					return
				}
				schedule = inputqueue.ReplaceSchedule(scheduledAt)
			}
			if sendQueueError(sender, svc.refuseUnreadableAttachments(r.GetAgentId(), r.GetAttachments())) {
				return
			}
			snapshot, err := svc.InputQueue.Update(bgCtx(), inputqueue.UpdateRequest{
				AgentID:         r.GetAgentId(),
				InputID:         r.GetInputId(),
				ClientID:        r.GetClientId(),
				ExpectedVersion: r.GetExpectedVersion(),
				Text:            r.GetText(),
				Attachments:     queueAttachments(r.GetAttachments()),
				Schedule:        schedule,
			})
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.UpdateQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceCancelQueuedAgentInputEditProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.CancelQueuedAgentInputEditRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.CancelEdit(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetClientId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.CancelQueuedAgentInputEditResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.DeleteQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Delete(bgCtx(), r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.DeleteQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceMoveQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.MoveQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Move(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetBeforeInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.MoveQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceSetAgentInputQueuePausedProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.SetAgentInputQueuePausedRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.SetPaused(bgCtx(), r.GetAgentId(), r.GetPaused())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.SetAgentInputQueuePausedResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.SteerQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Steer(bgCtx(), r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.SteerQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.PreemptQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Preempt(bgCtx(), r.GetAgentId(), r.GetInputId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.PreemptQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceAdvanceAgentInputQueueProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.AdvanceAgentInputQueueRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Advance(bgCtx(), r.GetAgentId(), func(agentID string) bool {
				return svc.inputProcessRecovery(agentID) != nil
			})
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.AdvanceAgentInputQueueResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})

	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceRetryQueuedAgentInputProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, r *leapmuxv1.RetryQueuedAgentInputRequest, sender channel.ResponseWriter) {
			snapshot, err := svc.InputQueue.Retry(bgCtx(), r.GetAgentId(), r.GetInputId(), r.GetConfirmDeliveryUncertain())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.RetryQueuedAgentInputResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})
}

func sendQueueError(sender channel.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, inputqueue.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		sendNotFoundError(sender, err.Error())
	case errors.Is(err, inputqueue.ErrConflict), errors.Is(err, inputqueue.ErrEditOwned),
		errors.Is(err, agent.ErrInputProcessRestartRequired),
		errors.Is(err, inputqueue.ErrEditOwnerMismatch), errors.Is(err, inputqueue.ErrVersionConflict),
		errors.Is(err, inputqueue.ErrNotHead), errors.Is(err, inputqueue.ErrRetryState),
		errors.Is(err, inputqueue.ErrUncertainConfirmation), errors.Is(err, inputqueue.ErrTurnEnded),
		errors.Is(err, inputqueue.ErrSteeringState), errors.Is(err, inputqueue.ErrSteeringUnsupported),
		errors.Is(err, inputqueue.ErrScheduled),
		errors.Is(err, inputqueue.ErrPreemptionUnsupported), errors.Is(err, inputqueue.ErrPreemptionState),
		errors.Is(err, inputqueue.ErrScheduleOrder), errors.Is(err, inputqueue.ErrManagerStopped),
		errors.Is(err, inputqueue.ErrAgentMessageEdit):
		sendFailedPrecondition(sender, err.Error())
	case errors.Is(err, inputqueue.ErrInvalidInput), errors.Is(err, inputqueue.ErrQueueFull),
		errors.Is(err, inputqueue.ErrItemTooLarge), errors.Is(err, inputqueue.ErrQueueAttachmentsLarge):
		sendInvalidArgument(sender, err.Error())
	default:
		slog.Error("agent input queue operation failed", "error", err)
		sendInternalError(sender, "agent input queue operation failed")
	}
	return true
}
