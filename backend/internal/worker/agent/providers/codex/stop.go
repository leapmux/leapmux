package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"golang.org/x/sync/errgroup"
)

const (
	codexListCommandProcessesMethod    = "thread/backgroundTerminals/list"
	codexTerminateCommandProcessMethod = "thread/backgroundTerminals/terminate"
	codexCommandPageSize               = 100
	codexCommandPageLimit              = 128
	codexCommandStopConcurrency        = 4
)

type codexCommandProcess struct {
	ItemID    string `json:"itemId"`
	ProcessID string `json:"processId"`
}

type codexCommandRPC func(string, any) (json.RawMessage, error)

// commandIDsForTurnLocked captures only commands that the requested turn owns.
// Older commands and other threads must remain untouched. The caller holds Mu.
func (a *Agent) commandIDsForTurnLocked(threadID, turnID string) map[string]struct{} {
	commands := make(map[string]struct{})
	for itemID, tool := range a.incompleteTools {
		if tool == nil || tool.itemType != contracts.CodexItemTypeCommandExecution {
			continue
		}
		event, ok := newCodexItemEvent(nil, tool.params)
		if ok && event.itemID == itemID && event.threadID == threadID && event.turnID == turnID {
			commands[itemID] = struct{}{}
		}
	}
	return commands
}

// rememberCommandForStopLocked keeps commands announced while a native interrupt awaits its reply.
// The caller holds Mu. Turn completion can remove the normal incomplete-tool record before that reply arrives.
func (a *Agent) rememberCommandForStopLocked(itemID, itemType string, params json.RawMessage) {
	if itemType != contracts.CodexItemTypeCommandExecution {
		return
	}
	event, ok := newCodexItemEvent(nil, params)
	if !ok || event.itemID != itemID || event.itemType != contracts.CodexItemTypeCommandExecution {
		return
	}
	call := a.interruptCalls[codexInterruptKey{threadID: event.threadID, turnID: event.turnID}]
	if call != nil {
		if call.commands == nil {
			call.commands = make(map[string]struct{})
		}
		call.commands[itemID] = struct{}{}
	}
}

// stopCodexCommands uses native process IDs because command sessions outlive a cancelled model turn.
// Read every page before terminating anything, so our own removals cannot shift the native cursor.
func (a *Agent) stopCodexCommands(threadID string, commands map[string]struct{}) error {
	if len(commands) == 0 {
		return nil
	}
	deadline := time.Now().Add(a.APITimeout())
	request := func(method string, params any) (json.RawMessage, error) {
		timeout := time.Until(deadline)
		if timeout <= 0 {
			return nil, fmt.Errorf("%s: %w", method, context.DeadlineExceeded)
		}
		body, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", method, err)
		}
		response, err := a.SendRequest(method, body, timeout)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		return response, nil
	}
	processes, err := readCodexCommandProcesses(threadID, request)
	if err != nil {
		return err
	}
	var targets []codexCommandProcess
	for _, process := range processes {
		if _, owned := commands[process.ItemID]; owned {
			targets = append(targets, process)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ProcessID < targets[j].ProcessID })
	var group errgroup.Group
	group.SetLimit(codexCommandStopConcurrency)
	failures := make([]error, len(targets))
	for index, process := range targets {
		group.Go(func() error {
			failures[index] = terminateCodexCommandProcess(threadID, process, request)
			return nil
		})
	}
	_ = group.Wait() // Each worker stores its error at its own index.
	return errors.Join(failures...)
}

func readCodexCommandProcesses(threadID string, request codexCommandRPC) (map[string]codexCommandProcess, error) {
	processes := make(map[string]codexCommandProcess)
	seenCursors := make(map[string]struct{})
	var cursor *string
	for page := 0; page < codexCommandPageLimit; page++ {
		params := struct {
			ThreadID string  `json:"threadId"`
			Cursor   *string `json:"cursor,omitempty"`
			Limit    int     `json:"limit"`
		}{ThreadID: threadID, Cursor: cursor, Limit: codexCommandPageSize}
		raw, err := request(codexListCommandProcessesMethod, params)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data       *[]codexCommandProcess `json:"data"`
			NextCursor *string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, fmt.Errorf("read native command list: %w", err)
		}
		if response.Data == nil {
			return nil, errors.New("the native command list omits its data")
		}
		for _, process := range *response.Data {
			id, err := strconv.ParseInt(process.ProcessID, 10, 32)
			if strings.TrimSpace(process.ItemID) == "" || err != nil || strconv.FormatInt(id, 10) != process.ProcessID {
				return nil, errors.New("the native command list contains an invalid identity")
			}
			if prior, exists := processes[process.ProcessID]; exists && prior.ItemID != process.ItemID {
				return nil, errors.New("the native command process has conflicting owners")
			}
			processes[process.ProcessID] = process
		}
		if response.NextCursor == nil {
			return processes, nil
		}
		if *response.NextCursor == "" || len(*response.Data) == 0 {
			return nil, errors.New("the native command list has an invalid continuation")
		}
		if _, repeated := seenCursors[*response.NextCursor]; repeated {
			return nil, errors.New("the native command list repeats a cursor")
		}
		seenCursors[*response.NextCursor] = struct{}{}
		cursor = response.NextCursor
	}
	return nil, errors.New("the native command list exceeds the page limit")
}

func terminateCodexCommandProcess(threadID string, process codexCommandProcess, request codexCommandRPC) error {
	raw, err := request(codexTerminateCommandProcessMethod, struct {
		ThreadID  string `json:"threadId"`
		ProcessID string `json:"processId"`
	}{ThreadID: threadID, ProcessID: process.ProcessID})
	if err != nil {
		return err
	}
	var response struct {
		Terminated *bool `json:"terminated"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("read native command termination: %w", err)
	}
	if response.Terminated == nil {
		return errors.New("the native command termination omits its result")
	}
	if *response.Terminated {
		return nil
	}
	// A command can exit between the list and termination. Confirm absence without retrying termination.
	remaining, err := readCodexCommandProcesses(threadID, request)
	if err != nil {
		return err
	}
	if _, active := remaining[process.ProcessID]; active {
		return fmt.Errorf("native command process %q remains active", process.ProcessID)
	}
	return nil
}
