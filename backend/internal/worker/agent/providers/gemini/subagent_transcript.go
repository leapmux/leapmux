package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const geminiChildPollInterval = 250 * time.Millisecond

type geminiChildTranscript struct {
	services      agent.ProviderServices
	query         agent.StoredSessionQuery
	rootSessionID string
	rootAgentID   string
	clock         quartz.Clock
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	wake          chan struct{}
	lifecycleMu   sync.Mutex
	started       bool
	closed        bool
	stopOnce      sync.Once
	activityMu    sync.Mutex
	activeCalls   map[string]bool
	passMu        sync.Mutex
	children      map[string]string
	files         map[string]os.FileInfo
}

func newGeminiChildTranscript(ctx context.Context, clock quartz.Clock, services agent.ProviderServices, query agent.StoredSessionQuery, rootSessionID, rootAgentID string) *geminiChildTranscript {
	ctx, cancel := context.WithCancel(ctx)
	return &geminiChildTranscript{services: services, query: query, rootSessionID: rootSessionID, rootAgentID: rootAgentID, clock: clock, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), activeCalls: make(map[string]bool), children: make(map[string]string), files: make(map[string]os.FileInfo)}
}

func (transcript *geminiChildTranscript) start() {
	transcript.lifecycleMu.Lock()
	if transcript.started || transcript.closed {
		transcript.lifecycleMu.Unlock()
		return
	}
	transcript.started = true
	transcript.lifecycleMu.Unlock()
	go func() {
		defer close(transcript.done)
		transcript.flush(false, agent.MessageCompletionComplete)
		for {
			select {
			case <-transcript.ctx.Done():
				return
			case <-transcript.wake:
			}
			transcript.flush(false, agent.MessageCompletionComplete)
			if !transcript.active() {
				continue
			}
			ticker := transcript.clock.NewTicker(geminiChildPollInterval, "gemini", "child-transcript")
			for transcript.active() {
				select {
				case <-transcript.ctx.Done():
					ticker.Stop()
					return
				case <-transcript.wake:
				case <-ticker.C:
				}
				transcript.flush(false, agent.MessageCompletionComplete)
			}
			ticker.Stop()
		}
	}()
}

func (transcript *geminiChildTranscript) active() bool {
	transcript.activityMu.Lock()
	defer transcript.activityMu.Unlock()
	return len(transcript.activeCalls) > 0
}

// observe follows real root invocation phases. An idle root needs no poll timer.
func (transcript *geminiChildTranscript) observe(sessionID string, update json.RawMessage) {
	if sessionID != transcript.rootSessionID {
		return
	}
	var frame struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Status        string `json:"status"`
	}
	if json.Unmarshal(update, &frame) != nil || (frame.SessionUpdate != "tool_call" && frame.SessionUpdate != "tool_call_update") || !strings.HasPrefix(frame.ToolCallID, contracts.GeminiToolInvokeAgent+"__") {
		return
	}
	transcript.activityMu.Lock()
	switch frame.Status {
	case "completed", "failed", "cancelled":
		delete(transcript.activeCalls, frame.ToolCallID)
	default:
		transcript.activeCalls[frame.ToolCallID] = true
	}
	transcript.activityMu.Unlock()
	select {
	case transcript.wake <- struct{}{}:
	default:
	}
}

func (transcript *geminiChildTranscript) stop(completion agent.MessageCompletion) {
	transcript.stopOnce.Do(func() {
		transcript.lifecycleMu.Lock()
		transcript.closed = true
		transcript.cancel()
		started := transcript.started
		if !started {
			close(transcript.done)
		}
		transcript.lifecycleMu.Unlock()
		if started {
			<-transcript.done
			transcript.flush(true, completion)
		}
	})
}

func (transcript *geminiChildTranscript) flush(final bool, completion agent.MessageCompletion) {
	transcript.passMu.Lock()
	defer transcript.passMu.Unlock()
	records, err := geminiChildRecords(transcript.query, transcript.rootSessionID)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, sessionstore.ErrAbsent) {
		slog.Warn("read Gemini child records", "agent_id", transcript.rootAgentID, "error", err)
	}
	for _, record := range records {
		id := record.Session.SessionID
		prior := transcript.files[id]
		if prior != nil && os.SameFile(prior, record.Info) && prior.Size() == record.Info.Size() && prior.ModTime().Equal(record.Info.ModTime()) {
			continue
		}
		if err := transcript.persist(record.Session); err != nil {
			slog.Error("store Gemini child records", "agent_id", transcript.rootAgentID, "child_id", id, "error", err)
			continue
		}
		transcript.files[id] = record.Info
	}
	if final {
		status := bgtask.StatusFailed
		if completion == agent.MessageCompletionInterrupted {
			status = bgtask.StatusInterrupted
		}
		for key, childID := range transcript.children {
			if err := transcript.services.CloseBackgroundTask(key, status); err != nil {
				slog.Error("close Gemini child task", "agent_id", transcript.rootAgentID, "child_id", key, "error", err)
				continue
			}
			transcript.services.CleanupChildAgent(childID)
		}
	}
}

func (a *Agent) startChildTranscript(services agent.ProviderServices, query agent.StoredSessionQuery, sessionID string) {
	if sessionID == "" {
		return
	}
	a.childMu.Lock()
	if a.children != nil && a.children.rootSessionID == sessionID {
		a.childMu.Unlock()
		return
	}
	previous := a.children
	current := newGeminiChildTranscript(a.Context(), a.Clock(), services, query, sessionID, a.AgentID())
	a.children = current
	a.childMu.Unlock()
	if previous != nil {
		previous.stop(agent.MessageCompletionInterrupted)
	}
	current.start()
}

func (a *Agent) stopChildTranscript(completion agent.MessageCompletion) {
	a.childMu.Lock()
	previous := a.children
	a.children = nil
	a.childMu.Unlock()
	if previous != nil {
		previous.stop(completion)
	}
}

func (a *Agent) finishNativeChildren(completion agent.MessageCompletion) {
	a.childMu.Lock()
	children := a.children
	a.childMu.Unlock()
	if children != nil {
		children.flush(true, completion)
	}
}

func (transcript *geminiChildTranscript) persist(session geminiSession) error {
	writes, err := geminiChildWrites(session)
	if err != nil {
		return err
	}
	title := ""
	for _, message := range session.Messages {
		if message.Type == "user" {
			title = geminiMessageText(message.Content)
			break
		}
	}
	childID, err := transcript.services.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: session.SessionID, Title: title})
	if err != nil {
		return err
	}
	if childID == "" {
		return errors.New("the Gemini child has no Worker identity")
	}
	transcript.children[session.SessionID] = childID
	if err := transcript.services.UpsertBackgroundTask(bgtask.Upsert{RowKey: session.SessionID, Kind: bgtask.KindSubagent, ChildAgentID: childID, Title: title, Status: bgtask.StatusRunning}); err != nil {
		return err
	}
	child := transcript.services.ChildSink(childID)
	if child == nil {
		return errors.New("the Gemini child has no transcript service")
	}
	for _, write := range writes {
		content := write.Content
		content.IdempotencyKey = write.Key
		content.Completion = agent.MessageCompletionComplete
		if err := child.PersistMessage(write.Source, content, write.Span); err != nil {
			return err
		}
	}
	complete := false
	for _, message := range session.Messages {
		for _, raw := range message.ToolCalls {
			var tool geminiToolIdentity
			if json.Unmarshal(raw, &tool) == nil && tool.Name == contracts.GeminiToolCompleteTask && tool.Status == "success" {
				complete = true
			}
		}
	}
	if complete {
		if err := transcript.services.CloseBackgroundTask(session.SessionID, bgtask.StatusCompleted); err != nil {
			return err
		}
		transcript.services.CleanupChildAgent(childID)
	}
	return nil
}
