package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	openCodeShellPollInterval = 250 * time.Millisecond
	openCodeShellPollTimerTag = "opencode-shell-output-poll"
	openCodeShellPageSize     = 64 * 1024
	openCodeShellTailSize     = 8192
)

type openCodeShellKey struct{ sessionID, spanID string }
type openCodeShellReader struct {
	id       string
	cancel   context.CancelFunc
	location openCodeLocation
}

func (a *Agent) observeNativeShellMetadataLocked(run *openCodeRun, tool *openCodeToolState, raw json.RawMessage) error {
	if tool.name != contracts.OpenCodeNativeToolShell || tool.closed || !tool.opened || run.ctx.Err() != nil {
		return nil
	}
	var metadata struct {
		ShellID string `json:"shellID"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	if metadata.ShellID == "" {
		return nil
	}
	if err := openCodeRouteID(metadata.ShellID); err != nil {
		return err
	}
	key := openCodeShellKey{sessionID: run.id, spanID: tool.spanID}
	a.shellMu.Lock()
	defer a.shellMu.Unlock()
	if a.shellsClosed {
		return nil
	}
	if existing := a.shellReaders[key]; existing != nil {
		if existing.id != metadata.ShellID {
			return errors.New("the native shell tool changed its shell ID")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(run.ctx)
	run.mu.Lock()
	location := run.location
	run.mu.Unlock()
	reader := &openCodeShellReader{id: metadata.ShellID, cancel: cancel, location: location}
	if a.shellReaders == nil {
		a.shellReaders = make(map[openCodeShellKey]*openCodeShellReader)
	}
	a.shellReaders[key] = reader
	a.shellWorkers.Add(1)
	go func() {
		defer a.shellWorkers.Done()
		defer func() {
			cancel()
			a.shellMu.Lock()
			if a.shellReaders[key] == reader {
				delete(a.shellReaders, key)
			}
			a.shellMu.Unlock()
		}()
		a.readNativeShellOutput(ctx, run, tool, reader)
	}()
	return nil
}

func (a *Agent) stopNativeShellReader(run *openCodeRun, tool *openCodeToolState) {
	a.shellMu.Lock()
	reader := a.shellReaders[openCodeShellKey{sessionID: run.id, spanID: tool.spanID}]
	a.shellMu.Unlock()
	if reader != nil {
		reader.cancel()
	}
	if tool.name == contracts.OpenCodeNativeToolShell {
		run.sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
	}
}

// Stop every reader before closing the HTTP transport. No caller holds dispatch.
func (a *Agent) closeNativeShellReaders() {
	a.shellMu.Lock()
	a.shellsClosed = true
	for _, reader := range a.shellReaders {
		reader.cancel()
	}
	a.shellMu.Unlock()
	a.shellWorkers.Wait()
}

func (a *Agent) ownsNativeShellToolLocked(run *openCodeRun, tool *openCodeToolState) bool {
	a.Mu.Lock()
	current := !a.StoppedLocked() && a.runs[run.id] == run
	a.Mu.Unlock()
	return current && run.ctx.Err() == nil && !tool.closed && run.tools.tools[tool.key] == tool
}

func (a *Agent) captureNativeShellOutput(run *openCodeRun, tool *openCodeToolState) agent.TranscriptPublication {
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	if !a.ownsNativeShellToolLocked(run, tool) {
		return nil
	}
	content := run.sink.CaptureMessage(agent.MessageContent{AgentSessionID: run.id}, agent.SpanInfo{SpanID: tool.spanID, SpanType: tool.name})
	return content.Publication
}

func (a *Agent) publishNativeShellOutput(run *openCodeRun, tool *openCodeToolState, publication agent.TranscriptPublication, page openCodeShellOutput, tail string, lost bool) bool {
	a.dispatch.Lock()
	defer a.dispatch.Unlock()
	if !a.ownsNativeShellToolLocked(run, tool) {
		return false
	}
	publication.ReportProgress(agent.OutputExactTotalProgress(tool.spanID, page.Size))
	if page.Output != "" {
		publication.ReportProgress(agent.OutputTailProgress(tool.spanID, tail, lost))
	}
	return true
}

// Native tool progress identifies the shell. Its output API pages captured bytes.
func (a *Agent) readNativeShellOutput(ctx context.Context, run *openCodeRun, tool *openCodeToolState, reader *openCodeShellReader) {
	shellID, location := reader.id, reader.location
	var cursor int64
	var tail string
	var lost, verified bool
	for ctx.Err() == nil {
		publication := a.captureNativeShellOutput(run, tool)
		if publication == nil {
			return
		}
		if !verified {
			info, err := a.rpc.shellInfo(ctx, shellID, location)
			if err != nil {
				if ctx.Err() == nil {
					slog.Debug("OpenCode shell metadata read failed", "shell_id", shellID, "error", err)
				}
				if !a.waitNativeShellPoll(ctx) {
					return
				}
				continue
			}
			if info.ID != shellID || info.Metadata.SessionID != run.id {
				slog.Warn("OpenCode shell output belongs to another native owner", "shell_id", shellID, "session_id", run.id)
				return
			}
			verified = true
		}
		page, err := a.rpc.shellOutput(ctx, shellID, location, cursor)
		if err != nil {
			if ctx.Err() == nil {
				slog.Debug("OpenCode shell output read failed", "shell_id", shellID, "error", err)
			}
			if !a.waitNativeShellPoll(ctx) {
				return
			}
			continue
		}
		if page.Cursor < cursor || page.Size < page.Cursor || (page.Cursor == cursor && (page.Output != "" || page.Cursor < page.Size)) {
			slog.Warn("OpenCode shell output has an invalid byte cursor", "shell_id", shellID, "cursor", page.Cursor, "size", page.Size)
			return
		}
		var clipped bool
		tail, clipped = agent.ClipTailBytes(tail+page.Output, openCodeShellTailSize)
		lost = lost || clipped || page.Truncated
		if !a.publishNativeShellOutput(run, tool, publication, page, tail, lost) {
			return
		}
		cursor = page.Cursor
		if cursor == page.Size && !a.waitNativeShellPoll(ctx) {
			return
		}
	}
}

func (a *Agent) waitNativeShellPoll(ctx context.Context) bool {
	timer := a.Clock().NewTimer(openCodeShellPollInterval, openCodeShellPollTimerTag)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
