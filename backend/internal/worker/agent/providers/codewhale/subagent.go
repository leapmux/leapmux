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
// The model starts a subagent with the `agent` tool (`action: start`), and the
// call returns at once with the child's `agent_id` while the child runs in the
// background. The runtime HAS `agent.spawned`, `agent.progress` and
// `agent.completed` events, and it drops every one of them before it reaches a
// runtime thread: the monitor keeps an `agent.*` event only when the child's
// owner session equals the thread id, and a runtime engine runs under a random
// session id. So the thread's own stream says nothing about a child after its
// start call.
//
// Two other sources say everything:
//
//   - The runtime writes each child's transcript, message by message, to
//     `<workspace>/.codewhale/state/subagent-transcripts/<sha256(agent_id)>.jsonl`.
//     The worker reads it READ-ONLY and persists each content block of it as a
//     row of the child transcript.
//   - GET /v1/agent-runs/{agent_id} states the child's status and its final
//     summary.
//
// One watcher goroutine for each child reads both until the child ends. The
// parent's `agent` wait call also states which children settled, and that
// wakes the watcher at once.
//
// A `workflow` call starts children of its own. The runtime states its run id
// and status on each workflow call's result, and those become one workflow row
// in the registry. Its children get no transcript tab, for the reason the
// Claude provider gives its workflow runs none: nothing ties a workflow child
// to a spawn row of its own in this transcript.

// Watcher timing. The transcript is a local file, so reading it often is cheap;
// the run status is a request, so it is read less often.
const (
	childTailInterval = 500 * time.Millisecond
	childPollEvery    = 4
	// childMissingLimit is how many consecutive status reads may miss the run
	// before the watcher gives up on it. A just-started child can miss once;
	// a run the ledger never had misses for good.
	childMissingLimit = 30
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

// codewhaleChild is one subagent. The watcher goroutine owns every field below
// `nudge`; the rest is set once, before the watcher starts.
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
	missing   int
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

// finish forgets a child that ended.
func (c *codewhaleChildren) finish(agentID string) {
	c.mu.Lock()
	delete(c.byID, agentID)
	c.mu.Unlock()
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

// agentToolResult is the part of an `agent` result that the provider reads:
// the start receipt's id, and a wait's settled children. The metadata states
// the id as well, and a result carries whichever the runtime put there.
// contract_tags_test.go pins its tags, and those of settledRun, to the contract.
type agentToolResult struct {
	AgentID string       `json:"agent_id"`
	Settled []settledRun `json:"settled"`
}

// settledRun is one child that a wait reports as settled.
type settledRun struct {
	AgentID string `json:"agent_id"`
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
			a.children.wake(settled.AgentID)
		}
	}
}

// subagentTitle labels a child: its name, else its type, else its prompt's
// first line.
func subagentTitle(input agentToolInput) string {
	for _, candidate := range []string{input.Name, input.Type, bgtask.FirstLine(input.Prompt)} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// startChild opens a child transcript and a registry row for one subagent, and
// starts its watcher.
func (a *Agent) startChild(agentID, spawnSpan, title, prompt string) {
	childID, err := a.sink.EnsureChildAgent(spawnSpan, agentID, title)
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

// agentRunRecord is the part of GET /v1/agent-runs/{id} that the watcher reads.
type agentRunRecord struct {
	Status        string `json:"status"`
	ResultSummary string `json:"result_summary"`
}

// agentRunStatus maps a ledger status onto the registry. final is false for a
// run that still works, and for a word this build does not know: a final status
// is absorbing, so guessing one for a live child would close its row early.
//
// An interrupted child can continue from its checkpoint when the parent sends
// it a followup. Its row closes as stopped all the same, because the registry
// cannot open a final row again.
func agentRunStatus(word string) (status bgtask.Status, final bool) {
	switch word {
	case agentRunStatusCompleted:
		return bgtask.StatusCompleted, true
	case agentRunStatusFailed:
		return bgtask.StatusFailed, true
	case agentRunStatusCancelled, agentRunStatusInterrupted:
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusRunning, false
	}
}

// watchChild reads one child until it ends, or until ctx ends.
func (a *Agent) watchChild(ctx context.Context, child *codewhaleChild) {
	sink := a.sink.ChildSink(child.childID)
	for tick := 0; ; tick++ {
		a.tailChildTranscript(sink, child)
		if tick%childPollEvery == 0 {
			if a.pollChild(ctx, sink, child) {
				return
			}
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
			// A wake means the parent saw the child settle: read its status now.
			tick = -1
		case <-timer.C:
		}
	}
}

// pollChild reads the child's run status, and finishes the child when the run
// ended. It reports whether the child is finished. ctx is the watcher's.
func (a *Agent) pollChild(ctx context.Context, sink agent.ProviderServices, child *codewhaleChild) bool {
	var record agentRunRecord
	if err := a.readAgentRun(ctx, child.agentID, &record); err != nil {
		if ctx.Err() != nil {
			// The stop that ended the watcher closes the child's row.
			return false
		}
		if !providerkit.IsHTTPStatus(err, httpStatusNotFound) {
			slog.Debug("codewhale read a subagent run", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
			return false
		}
		child.missing++
		if child.missing < childMissingLimit {
			return false
		}
		// The run ledger does not know the child, and it never learned of it. The
		// transcript is all there is, and the child is not coming back.
		a.finishChild(sink, child, bgtask.StatusFailed, "")
		return true
	}
	child.missing = 0
	status, final := agentRunStatus(record.Status)
	if !final {
		return false
	}
	// The last messages land before the status turns final, so one more read
	// takes whatever arrived since the last tick.
	a.tailChildTranscript(sink, child)
	a.finishChild(sink, child, status, record.ResultSummary)
	return true
}

// finishChild closes one child's registry row, reports its summary into the
// PARENT transcript, and releases its transcript state.
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
	a.children.finish(child.agentID)
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

// tailChildTranscript reads what the runtime appended to the child's
// transcript since the last read, and persists each complete record.
//
// The file is in the user's workspace, which the worker does not own, so the
// read refuses anything but a regular file: the path is statted without
// following a link, and the file that opens must be that same file.
func (a *Agent) tailChildTranscript(sink agent.ProviderServices, child *codewhaleChild) {
	info, err := os.Lstat(child.path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if info.Size() < child.offset {
		// The file was replaced. Records are deduplicated by index, so a reread
		// from the start persists only what is new.
		child.offset = 0
		child.pending = nil
	}
	if info.Size() == child.offset {
		return
	}
	f, err := os.Open(child.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return
	}
	if _, err := f.Seek(child.offset, io.SeekStart); err != nil {
		return
	}
	chunk, err := io.ReadAll(io.LimitReader(f, childReadChunk))
	if err != nil || len(chunk) == 0 {
		return
	}
	child.offset += int64(len(chunk))
	data := append(child.pending, chunk...)
	for {
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSpace(data[:newline])
		data = data[newline+1:]
		if len(line) > 0 {
			a.persistChildRecord(sink, child, line)
		}
	}
	if len(data) > childMaxLine {
		slog.Warn("codewhale subagent transcript record too long; dropped", "agent_id", a.AgentID(), "child", child.agentID, "bytes", len(data))
		data = nil
	}
	child.pending = append([]byte(nil), data...)
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

// childBlockRow is the row the worker persists for ONE content block. It keeps
// the record's own shape -- `kind`, `index` and a `message` whose content holds
// the one block -- and adds the block's position, so a row never states an
// event the runtime did not write.
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

// persistChildRecord persists one transcript record.
//
// The first message is the child's assignment, which PersistChildPrompt already
// wrote as the transcript's first row. A later user text is a message the
// parent sent the child, and it lands as a user message.
func (a *Agent) persistChildRecord(sink agent.ProviderServices, child *codewhaleChild, line []byte) {
	var record transcriptRecord
	if err := json.Unmarshal(line, &record); err != nil {
		slog.Debug("codewhale subagent transcript record unreadable", "agent_id", a.AgentID(), "child", child.agentID, "error", err)
		return
	}
	switch record.Kind {
	case contracts.CodewhaleTranscriptKindHeader:
		return
	case contracts.CodewhaleTranscriptKindMessage:
	default:
		return
	}
	if record.Index == nil || *record.Index < child.nextIndex {
		return
	}
	index := *record.Index
	child.nextIndex = index + 1
	var message transcriptMessage
	if err := json.Unmarshal(record.Message, &message); err != nil {
		return
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
	// Ended is the runtime's `terminal` flag: the run ended, whatever its
	// status word states.
	Ended bool `json:"terminal"`
}

// workflowRunStatus maps a run status onto the registry. A degraded run
// returned a value, but one of its stages failed. A word this build does not
// know reads as running, which a later summary of the ended run corrects.
func workflowRunStatus(word string) bgtask.Status {
	switch word {
	case contracts.CodewhaleWorkflowStatusCompleted:
		return bgtask.StatusCompleted
	case contracts.CodewhaleWorkflowStatusDegraded, contracts.CodewhaleWorkflowStatusFailed:
		return bgtask.StatusFailed
	case contracts.CodewhaleWorkflowStatusCancelled:
		return bgtask.StatusStopped
	default:
		return bgtask.StatusRunning
	}
}

// observeWorkflowToolResult keeps one registry row for each workflow run. Every
// workflow call that states a run -- start, run and status -- updates it.
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
		status = bgtask.StatusCompleted
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

// --- background shells ---
//
// A shell job that runs in the background: a `task_shell_start` call, or a
// `bash` call that ran past its foreground wait and moved to the background.
// The call's result states the job in its metadata -- `task_id`, `backgrounded`
// and the status `Running` -- and the job becomes a shell row in the registry.
// The row key is the launching call's span, so the row leads to the call that
// started the job.
//
// The runtime reports the job's end to the MODEL, as a runtime event at the
// start of the next turn, and never on the thread's event stream. Three sources
// end the row instead:
//
//   - A later tool result that states the job's `task_id` and a final status:
//     `task_shell_wait`, or a `bash` wait.
//   - From 0.10.0, GET /v1/threads/{id}/jobs/{job_id}, which a poller reads
//     for each job while it runs. 0.9.13 has no jobs routes, and the poller
//     stops for the life of the agent, so there a job that the model never
//     waits for keeps a running row until the process exits.
//   - The process exit, which ends every job the runtime started.

// Shell job status words, the runtime's ShellStatus. The browser reads none of
// them, so they are not in the contract.
const (
	shellStatusRunning   = "Running"
	shellStatusCompleted = "Completed"
	shellStatusFailed    = "Failed"
	shellStatusKilled    = "Killed"
	shellStatusTimedOut  = "TimedOut"
)

// Shell job poll timing. The jobs route is a local request, and a job that ended
// shows as running for at most one interval.
const shellPollInterval = 2 * time.Second

// shellPollTimerTag labels the poller's timer for a test's clock trap.
const shellPollTimerTag = "codewhale-shell-poll"

// codewhaleShells indexes the background shell jobs that run, by task id. The
// caller holds Mu.
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

// shellJobStatus maps a job status onto the registry. final is false for a job
// that runs, and for a word this build does not know, because a final status
// is absorbing.
func shellJobStatus(word string) (status bgtask.Status, final bool) {
	switch word {
	case shellStatusCompleted:
		return bgtask.StatusCompleted, true
	case shellStatusFailed, shellStatusTimedOut:
		return bgtask.StatusFailed, true
	case shellStatusKilled:
		return bgtask.StatusStopped, true
	default:
		return bgtask.StatusRunning, false
	}
}

// observeShellResult opens a shell row for a job that a call started, and closes
// the row of a job whose end a call reports.
//
// Only a call whose input states a command opens a row. A wait on a job that
// still runs also reports it as backgrounded and running, and it starts nothing.
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

// pollShellJobs reads each job that runs, until no job runs, the jobs routes
// turn out to be missing, or ctx ends.
//
// It reads each job by its own route and never by the list. The runtime's list
// first drops every finished job that started more than an hour ago, and only
// then states the rest (`list_jobs`), so a list never states the end of a job
// that ran that long. A read of one job drops nothing. The list serves once, to
// learn whether the runtime has the jobs routes at all.
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

// probeJobsRoutes asks the list route whether the runtime serves the jobs
// routes, and records the answer. It reports whether the poller goes on: false
// for a missing route, and for a stop. An answer that establishes nothing is
// asked again at the next tick.
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

// pollShellJob reads one job, and closes its row when the job ended. It reports
// whether the poller goes on, which is false only for a stop.
func (a *Agent) pollShellJob(ctx context.Context, threadID, taskID string) bool {
	job, err := a.readThreadJob(ctx, threadID, taskID)
	if ctx.Err() != nil {
		return false
	}
	switch {
	case providerkit.IsHTTPStatus(err, httpStatusNotFound):
		// The runtime drops a job only after it ended, so a job that it no longer
		// knows ended between two reads. Its outcome went with it, and the row
		// closes as completed: a row that stays running would state a job that
		// no longer exists.
		a.finishShell(taskID, bgtask.StatusCompleted)
	case err != nil:
		slog.Debug("codewhale read a shell job", "agent_id", a.AgentID(), "task_id", taskID, "error", err)
	default:
		if status, final := shellJobStatus(job.Status); final {
			a.finishShell(taskID, status)
		}
	}
	return true
}

// closeOpenShells closes the row of every job that still runs, when the process
// exits: the runtime ends every job with the session that started it. Call it
// after children.stopAll, which stops the poller.
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
