package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var errPlanWriterExpired = errors.New("the captured plan write's native source expired")

type capturedPlanWriter struct{ operation planOperation }
type planOperation struct {
	agentID string
	source  *nativeSessionSource
	content agent.MessageContent
	writer  agent.TranscriptServices
}
type preparedPlan struct {
	content []byte
	title   string
	now     time.Time
}

// planState serializes file mutations and publication order without caching plan content.
type planState struct {
	mu       sync.Mutex
	pending  []*planPublication
	draining bool
}
type planPublication struct {
	operation    planOperation
	provider     leapmuxv1.AgentProvider
	payload      map[string]interface{}
	notification *agent.CapturedTranscript
}

func (sink *agentOutputSink) PlanWriterFor(captured agent.CapturedTranscript) (agent.CapturedPlanWriter, error) {
	source, err := sink.capturedNativeSessionSource(captured)
	if err != nil {
		return nil, err
	}
	writer := captured.TranscriptWriter()
	if writer == nil {
		return nil, errors.New("the captured plan has no transcript writer")
	}
	release := source.lock()
	current := source.currentLocked()
	release()
	if !current {
		return nil, errPlanWriterExpired
	}
	return &capturedPlanWriter{operation: planOperation{agentID: sink.agentID, source: &source, content: agent.MessageContent{AgentSessionID: captured.FrozenContent().AgentSessionID, Publication: captured.FrozenContent().Publication}, writer: writer}}, nil
}

func (writer *capturedPlanWriter) UpdatePlan(content []byte, compression leapmuxv1.ContentCompression, title string) error {
	return writer.operation.source.sink.h.updatePlanFrom(writer.operation, content, compression, title)
}

func (h *OutputHandler) planStateFor(agentID string) *planState {
	value, _ := h.planStates.LoadOrStore(agentID, &planState{})
	return value.(*planState)
}

// preparePlan invokes the configured clock before any operation or source lock.
func (h *OutputHandler) preparePlan(compressed []byte, compression leapmuxv1.ContentCompression, title string) (preparedPlan, error) {
	if len(compressed) == 0 {
		return preparedPlan{}, nil
	}
	content, err := msgcodec.Decompress(compressed, compression)
	if err != nil {
		return preparedPlan{}, fmt.Errorf("decompress the saved plan: %w", err)
	}
	if len(content) == 0 {
		return preparedPlan{}, nil
	}
	return preparedPlan{content: slices.Clone(content), title: title, now: h.now()}, nil
}

func (h *OutputHandler) updatePlanFrom(operation planOperation, compressed []byte, compression leapmuxv1.ContentCompression, title string) error {
	prepared, err := h.preparePlan(compressed, compression, title)
	if err != nil {
		return err
	}
	if len(prepared.content) == 0 {
		return nil
	}
	state := h.planStateFor(operation.agentID)
	state.mu.Lock()
	publication, warnings, err := func() (*planPublication, []error, error) {
		if operation.source != nil {
			release := operation.source.lock()
			defer release()
			if !operation.source.currentLocked() {
				return nil, nil, errPlanWriterExpired
			}
		}
		return h.commitPlanLocked(operation, prepared)
	}()
	if publication != nil {
		state.pending = append(state.pending, publication)
	}
	state.mu.Unlock()
	for _, warning := range warnings {
		slog.Warn("The Worker could not save the previous plan snapshot.", "agent_id", operation.agentID, "error", warning)
	}
	h.drainPlanPublications(operation.agentID, state)
	return err
}

// commitPlanLocked holds the operation mutex and any original native-source leases.
func (h *OutputHandler) commitPlanLocked(operation planOperation, prepared preparedPlan) (*planPublication, []error, error) {
	agentID := operation.agentID
	row, err := h.queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("read the agent for its saved plan: %w", err)
	}
	title := prepared.title
	if title == "" {
		title = row.PlanTitle
	}
	if title == row.PlanTitle && row.PlanFilePath != "" {
		if existing, readErr := os.ReadFile(row.PlanFilePath); readErr == nil && bytes.Equal(existing, prepared.content) {
			return nil, nil, nil
		}
	}
	dir, err := h.resolvePlanDir(row.PlanFilePath, prepared.now)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve the saved plan directory: %w", err)
	}
	var warnings []error
	snapshot := ""
	if row.PlanFilePath != "" {
		snapshot, err = h.snapshotPlanFile(row.PlanFilePath, prepared.now)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("snapshot %q: %w", row.PlanFilePath, err))
		}
	}
	path, err := writePlanFile(dir, title, prepared.content)
	if err != nil {
		return nil, warnings, errors.Join(fmt.Errorf("write the saved plan: %w", err), restorePlanFile(row.PlanFilePath, snapshot, ""))
	}
	titleChanged, pathChanged := title != row.PlanTitle, path != row.PlanFilePath
	autoRename := titleChanged && title != "" && title != row.Title && row.TitleAutoGenerated != 0
	if autoRename {
		err = h.queries.UpdateAgentPlanAndTitle(bgCtx(), db.UpdateAgentPlanAndTitleParams{PlanFilePath: path, PlanTitle: title, Title: title, ID: agentID})
	} else if titleChanged || pathChanged {
		err = h.queries.UpdateAgentPlan(bgCtx(), db.UpdateAgentPlanParams{PlanFilePath: path, PlanTitle: title, ID: agentID})
	}
	if err != nil {
		return nil, warnings, errors.Join(fmt.Errorf("store the saved plan path: %w", err), restorePlanFile(row.PlanFilePath, snapshot, path))
	}
	if !titleChanged && !pathChanged || title == "" {
		return nil, warnings, nil
	}
	payload := map[string]interface{}{contracts.NotificationFieldType: contracts.NotificationTypePlanUpdated, contracts.NotificationFieldPlanTitle: title, contracts.NotificationFieldPlanFilePath: path}
	if autoRename {
		payload[contracts.NotificationFieldUpdateAgentTitle] = true
	}
	return &planPublication{operation: operation, provider: row.AgentProvider, payload: payload}, warnings, nil
}

// restorePlanFile removes only the new file and restores the prior canonical path.
func restorePlanFile(prior, snapshot, created string) error {
	var failure error
	if created != "" {
		if err := os.Remove(created); err != nil && !errors.Is(err, os.ErrNotExist) {
			failure = fmt.Errorf("remove the refused plan file: %w", err)
		}
	}
	if snapshot != "" {
		if err := os.Rename(snapshot, prior); err != nil {
			failure = errors.Join(failure, fmt.Errorf("restore the previous canonical plan: %w", err))
		}
	}
	return failure
}

// drainPlanPublications invokes callbacks after releasing every operation and source lock.
func (h *OutputHandler) drainPlanPublications(agentID string, state *planState) {
	state.mu.Lock()
	if state.draining || len(state.pending) == 0 {
		state.mu.Unlock()
		return
	}
	state.draining = true
	state.mu.Unlock()
	claimed := true
	defer func() {
		if claimed {
			state.mu.Lock()
			state.draining = false
			state.mu.Unlock()
		}
	}()
	for {
		state.mu.Lock()
		if len(state.pending) == 0 {
			state.draining = false
			claimed = false
			state.mu.Unlock()
			return
		}
		publication := state.pending[0]
		state.mu.Unlock()
		if err := h.publishSavedPlan(publication); err != nil {
			slog.Warn("The Worker could not publish the saved plan.", "agent_id", agentID, "error", err)
			return
		}
		state.mu.Lock()
		state.pending[0] = nil
		state.pending = state.pending[1:]
		state.mu.Unlock()
	}
}

func (h *OutputHandler) publishSavedPlan(publication *planPublication) error {
	if publication.operation.writer != nil {
		if publication.notification == nil {
			data, err := json.Marshal(publication.payload)
			if err != nil {
				return fmt.Errorf("encode the saved plan notification: %w", err)
			}
			content := publication.operation.content.Clone()
			content.Original = data
			captured := agent.CaptureTranscript(publication.operation.writer, content, agent.SpanInfo{})
			publication.notification = &captured
		}
		_, err := publication.notification.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
		return err
	}
	data, err := json.Marshal(publication.payload)
	if err != nil {
		return fmt.Errorf("encode the saved plan notification: %w", err)
	}
	_, err = h.writeNotification(publication.operation.agentID, publication.provider, h.agents.Registry().Plugin(publication.provider), leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, agent.MessageContent{Original: data}, notificationThreaded)
	return err
}
