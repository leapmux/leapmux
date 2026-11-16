package kimi

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

const kimiTaskReportTimerTag = "kimi.task.report"

func (a *Agent) nativeTaskReportCurrent(sessionID string, address kimiTaskAddress, task *kimiTask) bool {
	return a.Context().Err() == nil && !a.StoppedLocked() && a.sessionID == sessionID && a.tasks[address] == task && task.ended && !task.reportComplete
}

// scheduleNativeTaskReport runs under dispatchMu after the registry outcome closes.
func (a *Agent) scheduleNativeTaskReport(address kimiTaskAddress) {
	a.Mu.Lock()
	task := a.tasks[address]
	if task == nil || task.reportComplete || !task.ended || task.childAgentID == "" {
		a.Mu.Unlock()
		return
	}
	if !task.reportReady && strings.TrimSpace(task.native.Summary) != "" {
		task.reportText, task.reportReady = task.native.Summary, true
	}
	sessionID, ready := a.sessionID, task.reportReady
	if !ready && (task.native.Outcome != "completed" || a.stream == nil) {
		a.Mu.Unlock()
		return
	}
	a.Mu.Unlock()
	if ready {
		complete, err := a.persistNativeTaskReport(sessionID, address, task)
		if err != nil {
			slog.Warn("persist Kimi task report", "agent_id", a.AgentID(), "task_id", address.taskID, "error", err)
		}
		if complete {
			return
		}
	}
	key := fmt.Sprintf("%q/%q/%q/%p", sessionID, address.ownerAgentID, address.taskID, task)
	a.taskReportWork.Run(key, func() { a.recoverNativeTaskReport(sessionID, address, task) })
}

func (a *Agent) recoverNativeTaskReport(sessionID string, address kimiTaskAddress, task *kimiTask) {
	backoff := time.Second
	for {
		a.Mu.Lock()
		current := a.nativeTaskReportCurrent(sessionID, address, task)
		ready := task.reportReady
		a.Mu.Unlock()
		if !current {
			return
		}
		var err error
		if !ready {
			ctx, cancel := a.requestContext()
			output, readErr := a.stream.readTaskOutput(ctx, sessionID, address)
			cancel()
			err = readErr
			if err == nil {
				text := output.text
				if output.offset > 0 {
					text = fmt.Sprintf("The task report is truncated. Kimi Code returned only the final %d bytes.\n\n%s", len(output.text), output.text)
				}
				a.Mu.Lock()
				if a.nativeTaskReportCurrent(sessionID, address, task) {
					task.reportText, task.reportReady = text, true
				}
				a.Mu.Unlock()
			}
		}
		if err == nil {
			a.dispatchMu.Lock()
			complete, writeErr := a.persistNativeTaskReport(sessionID, address, task)
			a.finishDispatch()
			if complete {
				return
			}
			err = writeErr
		}
		if a.Context().Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("recover Kimi task report", "agent_id", a.AgentID(), "task_id", address.taskID, "error", err)
		}
		timer := a.clock.NewTimer(backoff, kimiTaskReportTimerTag)
		select {
		case <-a.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// persistNativeTaskReport reserves both destinations while session ownership is stable.
// The caller holds dispatchMu. Repeated writes use the same task-scoped report ID.
func (a *Agent) persistNativeTaskReport(sessionID string, address kimiTaskAddress, task *kimiTask) (bool, error) {
	a.Mu.Lock()
	if !a.nativeTaskReportCurrent(sessionID, address, task) {
		a.Mu.Unlock()
		return true, nil
	}
	if !task.reportReady {
		a.Mu.Unlock()
		return false, nil
	}
	text, childAgentID, label, background := task.reportText, task.native.ChildAgentID, task.native.Description, task.native.Detached
	a.Mu.Unlock()
	if task.reportContent == nil && strings.TrimSpace(text) != "" {
		key := fmt.Sprintf("%q/%q/%q", sessionID, address.ownerAgentID, address.taskID)
		write := agent.SubagentReportWrite{ReportID: providerkit.SubagentReportContentID("kimi-task", key, text), Report: agent.SubagentReport{Label: label, Text: text}}
		content, present, err := agent.SubagentReportContent(write)
		if err != nil {
			return false, err
		}
		if present {
			task.reportContent = &content
		}
	}
	if task.reportContent != nil {
		parent := a.runSink(a.run(address.ownerAgentID))
		child := a.runSink(a.run(childAgentID))
		if parent == nil || child == nil {
			return false, nil
		}
		a.captureNativeTaskOutputAnchors(address, task)
		if !task.childReportRetained {
			if task.childReportCapture.TranscriptWriter() == nil {
				capture, err := captureKimiTaskOutput(task.childOutput, child, true, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, *task.reportContent)
				if err != nil {
					return false, err
				}
				task.childReportCapture = capture
			}
			_, err := task.childReportCapture.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
			if !agent.OutputRetained(err) {
				return false, err
			}
			task.childReportRetained = true
			task.childReportCapture = agent.CapturedTranscript{}
		}
		if background && !task.parentReportRetained {
			if task.parentReportCapture.TranscriptWriter() == nil {
				capture, err := captureKimiTaskOutput(task.parentOutput, parent, !a.taskParentOutputIsCurrent(address, task), leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, *task.reportContent)
				if err != nil {
					return false, err
				}
				task.parentReportCapture = capture
			}
			_, err := task.parentReportCapture.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
			if !agent.OutputRetained(err) {
				return false, err
			}
			task.parentReportRetained = true
			task.parentReportCapture = agent.CapturedTranscript{}
		}
	}
	task.reportContent = nil
	a.Mu.Lock()
	task.reportComplete, task.reportText = true, ""
	a.Mu.Unlock()
	return true, nil
}
