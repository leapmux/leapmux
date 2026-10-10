package codewhale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Codewhale's subagents and workflows.
//
// The model starts a background child through the agent tool with action: start.
// The result immediately supplies agent_id while the child continues.
// The runtime defines these child events:
//   - agent.spawned.
//   - agent.progress.
//   - agent.completed.
// Its monitor retains them only when the child's owner session equals the runtime thread ID.
// The runtime engine uses a random session ID, so the thread stream drops those events and supplies no later child lifecycle.
//
// The worker therefore reads two sources:
//   - <workspace>/.codewhale/state/subagent-transcripts/<sha256(agent_id)>.jsonl supplies each content block as the runtime writes it.
//     The worker reads this file without modifying it and persists the blocks in the child transcript.
//     A child states its run's answer there, and 0.10 states no terminal record beside it: the run ended
//     when the child's latest assistant message holds an answer and no tool call, which is the runtime's
//     own settled shape for an explore child.
//   - The parent's agent tool result, whose status words the contract states. Codewhale 0.9 exposed a
//     GET /v1/agent-runs ledger beside them; 0.10 serves that route from its launch governor, which lists
//     no child, so the result and the transcript are the whole truth.
//
// One watcher per child reads the transcript until the child ends.
// The parent's agent wait result identifies settled children and wakes their watchers immediately.
//
// A workflow starts its own children and reports its run ID and status in each result.
// Those values identify one workflow registry row.
// Its children get no transcript tab because no native linkage associates each workflow child with an individual spawn row here.

// The watcher rereads the inexpensive local transcript on this cadence.
const (
	childTailInterval = 500 * time.Millisecond
	// childReadChunk limits one read of a transcript file.
	childReadChunk = 4 << 20
	// childMaxLine limits one record of a transcript file. A record longer than
	// this is dropped rather than held without limit.
	childMaxLine = 16 << 20
)

// childTimerTag labels the watcher's tail timer for a test's clock trap.
const childTimerTag = "codewhale-child-tail"

// codewhaleChildren tracks the thread's subagents.
type codewhaleChildren struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	byID map[string]*codewhaleChild
}

func newCodewhaleChildren(ctx context.Context) *codewhaleChildren {
	ctx, cancel := context.WithCancel(ctx)
	return &codewhaleChildren{ctx: ctx, cancel: cancel, byID: make(map[string]*codewhaleChild)}
}

// codewhaleChild represents one subagent.
// The watcher goroutine owns every field below nudge.
// Startup sets the earlier fields once before starting the watcher.
type codewhaleChild struct {
	agentID   string
	childID   string
	spawnSpan string
	title     string
	path      string
	// nudge wakes the watcher before its next tick.
	nudge chan struct{}

	offset    int64
	pending   []byte
	nextIndex int
	openTools map[string]string
	// answer holds the text of the child's latest assistant message.
	answer string
}

// answerSoFar is the child's latest answer text, for a settle that closes the
// child from its parent's wait result.
func (child *codewhaleChild) answerSoFar() string {
	return child.answer
}

// stopAll ends every watcher and waits for each one. It is nil-safe and
// idempotent.
func (c *codewhaleChildren) stopAll() {
	if c == nil {
		return
	}
	c.cancel()
	c.wg.Wait()
}

// run starts a watcher that belongs to no one child: the shell job poller. It
// reports false after stopAll.
func (c *codewhaleChildren) run(watch func(context.Context)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		watch(c.ctx)
	}()
	return true
}

// start registers a child and runs its watcher. It reports false for a child
// that is already watched, or after stopAll.
func (c *codewhaleChildren) start(child *codewhaleChild, watch func(context.Context, *codewhaleChild)) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	if _, exists := c.byID[child.agentID]; exists {
		return false
	}
	c.byID[child.agentID] = child
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		watch(c.ctx, child)
	}()
	return true
}

// claim takes a child out of the registry so exactly one finalizer closes it.
// It returns nil for a child another finalizer already claimed.
func (c *codewhaleChildren) claim(agentID string) *codewhaleChild {
	c.mu.Lock()
	defer c.mu.Unlock()
	child := c.byID[agentID]
	delete(c.byID, agentID)
	return child
}

// settle finalizes a child from outside its watcher. It reports false for a
// child that is not watched or that another finalizer already claimed.
func (c *codewhaleChildren) settle(agentID string, finalize func(*codewhaleChild)) bool {
	c.mu.Lock()
	child := c.byID[agentID]
	if child != nil {
		delete(c.byID, agentID)
	}
	c.mu.Unlock()
	if child == nil {
		return false
	}
	finalize(child)
	return true
}

// wake nudges the watcher of one child.
func (c *codewhaleChildren) wake(agentID string) {
	c.mu.Lock()
	child := c.byID[agentID]
	c.mu.Unlock()
	if child == nil {
		return
	}
	select {
	case child.nudge <- struct{}{}:
	default:
	}
}

// drain removes and returns every child that is still watched. Call it after
// stopAll, when no watcher runs.
func (c *codewhaleChildren) drain() []*codewhaleChild {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	children := make([]*codewhaleChild, 0, len(c.byID))
	for id, child := range c.byID {
		children = append(children, child)
		delete(c.byID, id)
	}
	return children
}

// childTranscriptPath is where the runtime writes one child's transcript.
func childTranscriptPath(workspace, agentID string) string {
	sum := sha256.Sum256([]byte(agentID))
	return filepath.Join(workspace, ".codewhale", "state", "subagent-transcripts", hex.EncodeToString(sum[:])+".jsonl")
}

// --- the `agent` tool ---

// agentToolInput is the part of an `agent` call's input that the provider reads.
type agentToolInput struct {
	Action string `json:"action"`
	Prompt string `json:"prompt"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

// toolSpawnsSubagent reports whether a tool call starts a subagent. A spawn
// owns no span: its work lands in the child transcript.
func toolSpawnsSubagent(name string, input json.RawMessage) bool {
	if name != contracts.CodewhaleToolAgent {
		return false
	}
	var parsed agentToolInput
	return json.Unmarshal(input, &parsed) == nil && parsed.Action == contracts.CodewhaleAgentActionStart
}

// agentToolResult contains the native start receipt ID and the settled children from a wait result.
// Metadata can also supply that ID, and the decoder accepts the native source that the runtime returns.
// contract_tags_test.go verifies its tags and settledRun's tags against the contract.
type agentToolResult struct {
	AgentID string       `json:"agent_id"`
	Settled []settledRun `json:"settled"`
}

// settledRun is one child that a wait reports as settled.
type settledRun struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

// observeAgentToolResult starts a watcher for a started child, and wakes the
// watchers of the children a wait settled.
func (a *Agent) observeAgentToolResult(env codewhaleEnvelope, payload itemEventPayload, spanID string, input json.RawMessage) {
	var parsed agentToolInput
	if json.Unmarshal(input, &parsed) != nil {
		return
	}
	var result agentToolResult
	_ = json.Unmarshal([]byte(payload.Item.Detail), &result)
	switch parsed.Action {
	case contracts.CodewhaleAgentActionStart:
		if env.Event != contracts.CodewhaleEventItemCompleted {
			// The start failed, so no child runs. The call's own row says why.
			return
		}
		agentID := result.AgentID
		if agentID == "" {
			var metadata agentToolResult
			_ = json.Unmarshal(payload.Item.Metadata, &metadata)
			agentID = metadata.AgentID
		}
		if agentID == "" {
			slog.Warn("codewhale subagent start stated no agent id", "agent_id", a.AgentID(), "span", spanID)
			return
		}
		a.startChild(agentID, spanID, subagentTitle(parsed), parsed.Prompt)
	case contracts.CodewhaleAgentActionWait:
		for _, settled := range result.Settled {
			status, final := agentRunStatus(settled.Status)
			if final {
				a.settleChild(settled.AgentID, status)
				continue
			}
			// A resumable child keeps its open row under the word the wait saw.
			if err := a.sink.UpdateBackgroundTaskStatus(settled.AgentID, status, ""); err != nil {
				slog.Warn("codewhale update a subagent run status", "agent_id", a.AgentID(), "child", settled.AgentID, "error", err)
			}
			a.children.wake(settled.AgentID)
		}
	}
}

// settleChild closes one child from the status word of its parent's wait
// result. Its transcript keeps the answer, so the tail reads it once before
// the row closes.
func (a *Agent) settleChild(agentID string, status bgtask.Status) {
	a.children.settle(agentID, func(child *codewhaleChild) {
		sink := a.sink.ChildSink(child.childID)
		a.tailChildTranscript(sink, child)
		a.finishChild(sink, child, status, child.answerSoFar())
	})
}

// subagentTitle selects the first available child label in this order:
//   - Name.
//   - Type.
//   - The prompt's first line.
func subagentTitle(input agentToolInput) string {
	for _, candidate := range []string{input.Name, input.Type, bgtask.FirstLine(input.Prompt)} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// startChild creates the child transcript and registry row, then starts its watcher.
func (a *Agent) startChild(agentID, spawnSpan, title, prompt string) {
	childID, err := a.sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: spawnSpan, ProviderChildKey: agentID, Title: title})
	if err != nil {
		slog.Warn("codewhale open a child transcript", "agent_id", a.AgentID(), "child", agentID, "error", err)
		return
	}
	providerkit.LogRegistryRefusal(codewhaleProviderName, "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:       agentID,
		Kind:         bgtask.KindSubagent,
		ChildAgentID: childID,
		Title:        title,
		Description:  bgtask.FirstLine(prompt),
		Status:       bgtask.StatusRunning,
	}))
	if err := a.sink.PersistChildPrompt(childID, prompt); err != nil {
		slog.Warn("codewhale persist a child prompt", "agent_id", a.AgentID(), "child", agentID, "error", err)
	}
	child := &codewhaleChild{
		agentID:   agentID,
		childID:   childID,
		spawnSpan: spawnSpan,
		title:     title,
		path:      childTranscriptPath(a.workingDir, agentID),
		nudge:     make(chan struct{}, 1),
		openTools: make(map[string]string),
	}
	a.children.start(child, a.watchChild)
}

// --- the watcher ---

// agentRunStatus maps a result's status word to registry state.
// An interrupted child can resume from its checkpoint, so preserve its open row.
// An unknown native word also leaves the row open because a chosen final state cannot return to Running.
func agentRunStatus(word string) (status bgtask.Status, final bool) {
	switch word {
	case "queued", "starting":
		return bgtask.StatusPending, false
	case "waiting_for_user", agentRunStatusInterrupted:
		return bgtask.StatusPaused, false
	case agentRunStatusCompleted:
		return bgtask.StatusSucceeded, true
	case agentRunStatusFailed:
		return bgtask.StatusFailed, true
	case agentRunStatusCancelled:
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusRunning, false
	}
}

// watchChild reads one child's transcript until its run answers, or until ctx
// ends. The tail claims the child before closing it, so a settle that the
// parent's wait result claimed first leaves the tail nothing to close and the
// watcher exits on its next read.
func (a *Agent) watchChild(ctx context.Context, child *codewhaleChild) {
	sink := a.sink.ChildSink(child.childID)
	for {
		if a.tailChildTranscript(sink, child) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		timer := a.clock.NewTimer(childTailInterval, childTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-child.nudge:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// finishChild closes the child's registry row and writes its summary in the parent transcript.
// It then releases the child's transcript state.
func (a *Agent) finishChild(sink agent.ProviderServices, child *codewhaleChild, status bgtask.Status, summary string) {
	closeChildTools(sink, child)
	providerkit.LogRegistryRefusal(codewhaleProviderName, "close", a.sink.CloseBackgroundTask(child.agentID, status))
	if strings.TrimSpace(summary) != "" {
		providerkit.PersistSubagentReport(a.sink, agent.SubagentReportWrite{
			ReportID: providerkit.SubagentReportContentID(codewhaleProviderName, child.agentID, summary),
			Report:   agent.SubagentReport{Label: child.title, Text: summary, Status: bgtask.StatusWire(status)},
		})
	}
	a.sink.CleanupChildAgent(child.childID)
}

// closeOpenChildren closes the rows of the children a process exit cut off.
// Call it after children.stopAll.
func (a *Agent) closeOpenChildren(completion agent.MessageCompletion) {
	for _, child := range a.children.drain() {
		sink := a.sink.ChildSink(child.childID)
		closeChildTools(sink, child)
		providerkit.LogRegistryRefusal(codewhaleProviderName, "close", a.sink.CloseBackgroundTask(child.agentID, agent.IncompleteTaskStatus(completion)))
		a.sink.CleanupChildAgent(child.childID)
	}
}

// closeChildTools closes the spans of the child's tool calls that the
// transcript never answered.
func closeChildTools(sink agent.ProviderServices, child *codewhaleChild) {
	for toolID := range child.openTools {
		sink.CloseSpan(toolID)
		delete(child.openTools, toolID)
	}
}

// --- the transcript file ---

// tailChildTranscript reads data appended after its preceding read and persists each complete record.
//
// The user owns the workspace file, so accept only a regular file.
// Inspect the path without following a symbolic link, and require the opened file to match that inspected file.
func (a *Agent) tailChildTranscript(sink agent.ProviderServices, child *codewhaleChild) bool {
	info, err := os.Lstat(child.path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Size() < child.offset {
		// The file was replaced. Records are deduplicated by index, so a reread
		// from the start persists only what is new.
		child.offset = 0
		child.pending = nil
	}
	if info.Size() == child.offset {
		return false
	}
	f, err := os.Open(child.path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	if _, err := f.Seek(child.offset, io.SeekStart); err != nil {
		return false
	}
	chunk, err := io.ReadAll(io.LimitReader(f, childReadChunk))
	if err != nil || len(chunk) == 0 {
		return false
	}
	child.offset += int64(len(chunk))
	data := append(child.pending, chunk...)
	finished := false
	for {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(data[:newline])
		data = data[newline+1:]
		if len(line) > 0 {
			finished = a.persistChildRecord(sink, child, line) || finished
		}
	}
	if len(data) > childMaxLine {
		slog.Warn("codewhale subagent transcript record too long; dropped", "agent_id", a.AgentID(), "child", child.agentID, "bytes", len(data))
		data = nil
	}
	child.pending = append([]byte(nil), data...)
	return finished
}

// transcriptRecord is one line of a child transcript.
type transcriptRecord struct {
	Kind    string          `json:"kind"`
	AgentID string          `json:"agent_id"`
	Index   *int            `json:"index"`
	Message json.RawMessage `json:"message"`
}

// transcriptMessage is the message a record carries.
type transcriptMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// transcriptBlock is the part of a content block that routes it.
type transcriptBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	Text      string `json:"text"`
}

// childBlockRow preserves one native content block in a worker row.
// Keep the original kind and index, plus a message containing only that block.
// Add the block position so the row still represents an event that the runtime actually writes.
type childBlockRow struct {
	Kind    string          `json:"kind"`
	Index   int             `json:"index"`
	Block   int             `json:"block"`
	Message childRowMessage `json:"message"`
}

type childRowMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// persistChildRecord persists one native transcript record.
//
// PersistChildPrompt already records the child's first assignment as its opening row.
// Later user text represents a message from the parent and enters the transcript as an ordinary user message.
func (a *Agent) persistChildRecord(sink agent.ProviderServices, child *codewhaleChild, line []byte) bool {
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		slog.Debug("codewhale subagent transcript record unreadable", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
		return false
	}
	switch record.Kind {
	case contracts.CodewhaleTranscriptKindHeader:
		return false
	case contracts.CodewhaleTranscriptKindMessage:
	default:
		return false
	}
	if record.Index == nil || *record.Index < child.nextIndex {
		return false
	}
	index := *record.Index
	child.nextIndex = index + 1
	var message transcriptMessage
	if err := json.Unmarshal(record.Message, &message); err != nil {
		return false
	}
	callsTool := false
	answer := strings.Builder{}
	for _, raw := range message.Content {
		var block transcriptBlock
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		if block.Type == contracts.CodewhaleBlockTypeToolUse {
			callsTool = true
		}
		if message.Role == contracts.CodewhaleTranscriptRoleAssistant && block.Type == contracts.CodewhaleBlockTypeText {
			answer.WriteString(block.Text)
		}
	}
	for position, raw := range message.Content {
		var block transcriptBlock
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		if message.Role == contracts.CodewhaleTranscriptRoleUser && block.Type == contracts.CodewhaleBlockTypeText {
			if index > 0 && strings.TrimSpace(block.Text) != "" {
				if err := a.sink.PersistChildUserMessage(child.childID, block.Text); err != nil {
					slog.Warn("codewhale persist a child user message", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
				}
			}
			continue
		}
		row, err := json.Marshal(childBlockRow{
			Kind:    contracts.CodewhaleTranscriptKindMessage,
			Index:   index,
			Block:   position,
			Message: childRowMessage{Role: message.Role, Content: []json.RawMessage{raw}},
		})
		if err != nil {
			continue
		}
		a.persistChildBlock(sink, child, block, row)
	}
	if message.Role != contracts.CodewhaleTranscriptRoleAssistant {
		return false
	}
	if text := strings.TrimSpace(answer.String()); text != "" {
		child.answer = text
	}
	if callsTool {
		// The child asked for a tool, so its run continues after the tool's result.
		return false
	}
	// An assistant message that calls no tool is the run's answer: Codewhale
	// states no terminal record beside the transcript, and a settled explore
	// child ends on exactly this shape.
	if a.children.claim(child.agentID) == nil {
		// The parent's wait result already closed this child.
		return true
	}
	a.finishChild(sink, child, bgtask.StatusSucceeded, child.answer)
	return true
}

// persistChildBlock persists one block row, opening or closing the span of a
// tool call.
func (a *Agent) persistChildBlock(sink agent.ProviderServices, child *codewhaleChild, block transcriptBlock, row []byte) {
	content := agent.MessageContent{Original: row}
	switch block.Type {
	case contracts.CodewhaleBlockTypeToolUse:
		if block.ID == "" {
			break
		}
		child.openTools[block.ID] = block.Name
		if err := providerkit.OpenToolSpan(sink, content, block.ID, block.Name, false); err != nil {
			slog.Warn("codewhale persist a child tool call", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
		}
		return
	case contracts.CodewhaleBlockTypeToolResult:
		if block.ToolUseID == "" {
			break
		}
		name := child.openTools[block.ToolUseID]
		delete(child.openTools, block.ToolUseID)
		if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content,
			agent.SpanInfo{SpanID: block.ToolUseID, SpanType: name, Closing: true}); err != nil {
			slog.Warn("codewhale persist a child tool result", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
		}
		sink.CloseSpan(block.ToolUseID)
		return
	}
	if err := sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}); err != nil {
		slog.Warn("codewhale persist a child message", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
	}
}

// --- workflows ---

// workflowToolInput is the part of a `workflow` call's input that labels its run.
type workflowToolInput struct {
	Name string `json:"name"`
	Plan struct {
		Goal string `json:"goal"`
	} `json:"plan"`
}

// workflowResultMetadata is the run summary a `workflow` result carries.
type workflowResultMetadata struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	// Ended reads the native terminal flag, which states finality independently of the status word.
	Ended bool `json:"terminal"`
}

// workflowRunStatus maps native workflow state to registry state.
// A degraded run returns a value but includes at least one failed stage.
// Return Running for an unknown word.
// The result observer separately applies the native Ended flag, including an end without a known outcome.
func workflowRunStatus(word string) bgtask.Status {
	switch word {
	case contracts.CodewhaleWorkflowStatusCompleted:
		return bgtask.StatusSucceeded
	case contracts.CodewhaleWorkflowStatusDegraded, contracts.CodewhaleWorkflowStatusFailed:
		return bgtask.StatusFailed
	case contracts.CodewhaleWorkflowStatusCancelled:
		return bgtask.StatusStopped
	default:
		return bgtask.StatusRunning
	}
}

// observeWorkflowToolResult maintains one registry row per workflow run.
// A start, run, or status result updates that row whenever it identifies the run.
func (a *Agent) observeWorkflowToolResult(env codewhaleEnvelope, payload itemEventPayload, _ string, input json.RawMessage) {
	if env.Event != contracts.CodewhaleEventItemCompleted {
		return
	}
	var metadata workflowResultMetadata
	if json.Unmarshal(payload.Item.Metadata, &metadata) != nil || metadata.RunID == "" {
		return
	}
	var parsed workflowToolInput
	_ = json.Unmarshal(input, &parsed)
	title := strings.TrimSpace(parsed.Plan.Goal)
	if title == "" {
		title = strings.TrimSpace(parsed.Name)
	}
	status := workflowRunStatus(metadata.Status)
	if metadata.Ended && !status.IsFinished() {
		// The native end flag includes failed and cancelled runs.
		// An unknown status supplies no successful outcome.
		status = bgtask.StatusEndedWithUnknownOutcome
	}
	providerkit.LogRegistryRefusal(codewhaleProviderName, "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:     metadata.RunID,
		Kind:       bgtask.KindWorkflow,
		GroupKey:   metadata.RunID,
		GroupLabel: title,
		Title:      title,
		Status:     status,
	}))
}

// --- Background shells ---
//
// A background shell starts through task_shell_start or a bash call that exceeds its foreground wait.
// Its result metadata supplies task_id and backgrounded with status Running.
// Create a shell registry row under the launching call's span, so the row identifies its originating call.
//
// The runtime reports completion to the model at the next turn's start, without a thread-stream event.
// The worker closes the row through one of these sources:
//   - A later task_shell_wait or bash wait result with task_id and a final status.
//   - GET /v1/threads/{id}/jobs/{job_id}, available from Codewhale 0.10.0 and polled for each active job.
//   - Process exit, which ends every job that the runtime starts.
// Codewhale 0.9.13 has no jobs routes, so its poller stops for the process lifetime.
// Without a later model wait, that version retains the Running row until process exit.

// These ShellStatus words belong to Codewhale's native shell protocol.
// The browser does not interpret them, so they need no shared contract table.
const (
	shellStatusRunning   = "Running"
	shellStatusCompleted = "Completed"
	shellStatusFailed    = "Failed"
	shellStatusKilled    = "Killed"
	shellStatusTimedOut  = "TimedOut"
)

// shellPollInterval controls checks of the local jobs API.
// The next successful read observes an ended job; failed reads can delay that observation.
const shellPollInterval = 2 * time.Second

// shellPollTimerTag labels the poller's timer for a test's clock trap.
const shellPollTimerTag = "codewhale-shell-poll"

// codewhaleShells indexes active shell jobs by task ID.
// The caller holds Mu.
type codewhaleShells struct {
	// rowKeys maps a job's task id to its registry row key.
	rowKeys map[string]string
	// polling is true while a poller goroutine runs.
	polling bool
	// jobsRoutes states whether the runtime serves the jobs routes. The answer
	// does not change in the life of one process.
	jobsRoutes jobsRouteState
}

// jobsRouteState is what the poller knows about the jobs routes.
type jobsRouteState int

const (
	// jobsRoutesUnknown: the poller did not ask yet.
	jobsRoutesUnknown jobsRouteState = iota
	// jobsRoutesServed: the list route answered, so a 404 for one job means
	// that the runtime no longer knows the job.
	jobsRoutesServed
	// jobsRoutesMissing: the list route answered 404, as 0.9.13 does.
	jobsRoutesMissing
)

func (s *codewhaleShells) add(taskID, rowKey string) {
	if s.rowKeys == nil {
		s.rowKeys = make(map[string]string)
	}
	s.rowKeys[taskID] = rowKey
}

// take removes one job and returns its row key, or "" for a job that is not
// tracked.
func (s *codewhaleShells) take(taskID string) string {
	rowKey := s.rowKeys[taskID]
	delete(s.rowKeys, taskID)
	return rowKey
}

// shellResultMetadata is the part of a tool result's metadata that states a
// shell job.
type shellResultMetadata struct {
	TaskID       string `json:"task_id"`
	Backgrounded bool   `json:"backgrounded"`
	Status       string `json:"status"`
}

// shellLaunchInput is the part of a launching call's input that the row shows.
type shellLaunchInput struct {
	Command string `json:"command"`
}

// shellJob is the part of one job of the jobs routes that the poller reads.
type shellJob struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// shellJobStatus maps a native job status to registry state.
// Return final=false for Running or an unknown word because a chosen final registry state cannot return to Running.
func shellJobStatus(word string) (status bgtask.Status, final bool) {
	switch word {
	case shellStatusCompleted:
		return bgtask.StatusSucceeded, true
	case shellStatusFailed, shellStatusTimedOut:
		return bgtask.StatusFailed, true
	case shellStatusKilled:
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusRunning, false
	}
}

// observeShellResult opens a shell row for a launched job and closes it when a later result confirms its end.
//
// Open a row only when the call input supplies a command.
// A wait on an existing running job can repeat backgrounded and Running without starting another job.
func (a *Agent) observeShellResult(payload itemEventPayload, spanID string, input json.RawMessage) {
	var metadata shellResultMetadata
	if json.Unmarshal(payload.Item.Metadata, &metadata) != nil || metadata.TaskID == "" {
		return
	}
	if status, final := shellJobStatus(metadata.Status); final {
		a.finishShell(metadata.TaskID, status)
		return
	}
	if !metadata.Backgrounded || metadata.Status != shellStatusRunning {
		return
	}
	var launch shellLaunchInput
	_ = json.Unmarshal(input, &launch)
	command := strings.TrimSpace(launch.Command)
	if command == "" {
		return
	}
	a.Mu.Lock()
	if _, tracked := a.shells.rowKeys[metadata.TaskID]; tracked {
		a.Mu.Unlock()
		return
	}
	a.shells.add(metadata.TaskID, spanID)
	startPoller := !a.shells.polling && a.shells.jobsRoutes != jobsRoutesMissing
	if startPoller {
		a.shells.polling = true
	}
	a.Mu.Unlock()
	providerkit.LogRegistryRefusal(codewhaleProviderName, "upsert", a.sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:         spanID,
		Kind:           bgtask.KindShell,
		Title:          command,
		TitleIsCommand: true,
		Status:         bgtask.StatusRunning,
	}))
	if startPoller && !a.children.run(a.pollShellJobs) {
		a.Mu.Lock()
		a.shells.polling = false
		a.Mu.Unlock()
	}
}

// finishShell closes the row of one job, once.
func (a *Agent) finishShell(taskID string, status bgtask.Status) {
	a.Mu.Lock()
	rowKey := a.shells.take(taskID)
	a.Mu.Unlock()
	if rowKey == "" {
		return
	}
	providerkit.LogRegistryRefusal(codewhaleProviderName, "close", a.sink.CloseBackgroundTask(rowKey, status))
}

// pollShellJobs reads active jobs until none remain, the jobs routes are unavailable, or ctx ends.
//
// Use each individual job route instead of repeatedly listing jobs.
// In Codewhale 0.10.1, list_jobs runs cleanup before constructing its result.
// That cleanup expires final records by finished_at and enforces record and retained-byte limits.
// inspect_job performs no cleanup, so an individual read does not itself discard the final record before inspection.
// Other runtime operations can already remove a record; the per-job not-found path handles that missing outcome.
// Use the list once to determine whether jobs routes exist.
func (a *Agent) pollShellJobs(ctx context.Context) {
	for {
		a.Mu.Lock()
		if len(a.shells.rowKeys) == 0 {
			a.shells.polling = false
			a.Mu.Unlock()
			return
		}
		threadID := a.threadID
		routes := a.shells.jobsRoutes
		taskIDs := make([]string, 0, len(a.shells.rowKeys))
		for taskID := range a.shells.rowKeys {
			taskIDs = append(taskIDs, taskID)
		}
		a.Mu.Unlock()
		timer := a.clock.NewTimer(shellPollInterval, shellPollTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if routes == jobsRoutesUnknown && !a.probeJobsRoutes(ctx, threadID) {
			return
		}
		for _, taskID := range taskIDs {
			if !a.pollShellJob(ctx, threadID, taskID) {
				return
			}
		}
	}
}

// probeJobsRoutes reads the list endpoint and records whether the runtime supports jobs routes.
// Return false for a missing route or a stopped context.
// Retry an inconclusive response on the next tick.
func (a *Agent) probeJobsRoutes(ctx context.Context, threadID string) bool {
	err := a.listThreadJobs(ctx, threadID)
	if ctx.Err() != nil {
		// The stop that ended the poller closes the rows of the jobs.
		return false
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	switch {
	case err == nil:
		a.shells.jobsRoutes = jobsRoutesServed
		return true
	case providerkit.IsHTTPStatus(err, httpStatusNotFound):
		a.shells.jobsRoutes = jobsRoutesMissing
		a.shells.polling = false
		return false
	default:
		slog.Debug("codewhale read the shell jobs", "agent_id", a.AgentID(), "error", err)
		return true
	}
}

// pollShellJob reads one job and closes its row when the job ends.
// Return whether the poller continues, which is false only after a stop.
func (a *Agent) pollShellJob(ctx context.Context, threadID, taskID string) bool {
	job, err := a.readThreadJob(ctx, threadID, taskID)
	if ctx.Err() != nil {
		return false
	}
	switch {
	case providerkit.IsHTTPStatus(err, httpStatusNotFound):
		// The runtime can remove a job after any final outcome.
		// A missing known job ended, but the missing result supplies no successful outcome.
		a.finishShell(taskID, bgtask.StatusEndedWithUnknownOutcome)
	case err != nil:
		slog.Debug("codewhale read a shell job", "agent_id", a.AgentID(), "task_id", taskID, "error", err)
	default:
		if status, final := shellJobStatus(job.Status); final {
			a.finishShell(taskID, status)
		}
	}
	return true
}

// closeOpenShells closes every active job row at process exit because the runtime ends jobs with their owning session.
// Call it after children.stopAll stops the poller.
func (a *Agent) closeOpenShells() {
	a.Mu.Lock()
	rowKeys := a.shells.rowKeys
	a.shells.rowKeys = nil
	a.shells.polling = false
	a.Mu.Unlock()
	for _, rowKey := range rowKeys {
		providerkit.LogRegistryRefusal(codewhaleProviderName, "close", a.sink.CloseBackgroundTask(rowKey, bgtask.StatusStopped))
	}
}
