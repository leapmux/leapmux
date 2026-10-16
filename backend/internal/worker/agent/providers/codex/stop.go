package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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

type codexCommandClient interface {
	ListCommandProcesses(threadID string, cursor *string, limit int) (json.RawMessage, error)
	TerminateCommandProcess(threadID, processID string) (json.RawMessage, error)
}

type codexNativeCommandClient struct {
	agent    *Agent
	deadline time.Time
}

func (client codexNativeCommandClient) request(method string, params any) (json.RawMessage, error) {
	timeout := time.Until(client.deadline)
	if timeout <= 0 {
		return nil, fmt.Errorf("%s: %w", method, context.DeadlineExceeded)
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	response, err := client.agent.SendRequest(method, body, timeout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	return response, nil
}

func (client codexNativeCommandClient) ListCommandProcesses(threadID string, cursor *string, limit int) (json.RawMessage, error) {
	return client.request(codexListCommandProcessesMethod, struct {
		ThreadID string  `json:"threadId"`
		Cursor   *string `json:"cursor,omitempty"`
		Limit    int     `json:"limit"`
	}{ThreadID: threadID, Cursor: cursor, Limit: limit})
}

func (client codexNativeCommandClient) TerminateCommandProcess(threadID, processID string) (json.RawMessage, error) {
	return client.request(codexTerminateCommandProcessMethod, struct {
		ThreadID  string `json:"threadId"`
		ProcessID string `json:"processId"`
	}{ThreadID: threadID, ProcessID: processID})
}

func (a *Agent) interruptCodexTurn(threadID, turnID string) (bool, error) {
	if threadID == "" || turnID == "" {
		return false, nil
	}
	key := codexInterruptKey{threadID: threadID, turnID: turnID}
	a.Mu.Lock()
	if a.interruptCalls == nil {
		a.interruptCalls = make(map[codexInterruptKey]*codexInterruptCall)
	}
	if existing := a.interruptCalls[key]; existing != nil {
		done := existing.done
		a.Mu.Unlock()
		<-done
		return existing.accepted, existing.err
	}
	call := &codexInterruptCall{done: make(chan struct{}), commands: a.commandIDsForTurnLocked(threadID, turnID)}
	a.interruptCalls[key] = call
	a.Mu.Unlock()

	params, err := json.Marshal(map[string]string{"threadId": threadID, "turnId": turnID})
	if err == nil {
		_, err = a.SendRequest("turn/interrupt", params, a.APITimeout())
	}
	accepted := err == nil
	if err != nil {
		err = fmt.Errorf("turn/interrupt: %w", err)
	} else {
		a.Mu.Lock()
		commands := maps.Clone(call.commands)
		a.Mu.Unlock()
		err = a.stopCodexCommands(threadID, commands)
	}

	a.Mu.Lock()
	call.accepted = accepted
	call.err = err
	close(call.done)
	if err != nil {
		delete(a.interruptCalls, key)
	}
	a.Mu.Unlock()
	return accepted, err
}

func (a *Agent) clearInterruptCallsForThread(threadID string) {
	a.Mu.Lock()
	for key := range a.interruptCalls {
		if key.threadID == threadID {
			delete(a.interruptCalls, key)
		}
	}
	a.Mu.Unlock()
}

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
	client := codexNativeCommandClient{agent: a, deadline: time.Now().Add(a.APITimeout())}
	processes, err := readCodexCommandProcesses(threadID, client)
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
	return stopCodexCommandProcesses(threadID, targets, client)
}

// stopCodexCommandProcesses verifies all concurrent refusals against one final native snapshot.
func stopCodexCommandProcesses(threadID string, targets []codexCommandProcess, client codexCommandClient) error {
	var group errgroup.Group
	group.SetLimit(codexCommandStopConcurrency)
	failures := make([]error, len(targets))
	verify := make([]bool, len(targets))
	for index, process := range targets {
		group.Go(func() error {
			terminated, err := terminateCodexCommandProcess(threadID, process, client)
			failures[index] = err
			verify[index] = err == nil && !terminated
			return nil
		})
	}
	_ = group.Wait() // Each worker writes only its own entries.
	if slices.Contains(verify, true) {
		remaining, err := readCodexCommandProcesses(threadID, client)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for index, process := range targets {
			if !verify[index] {
				continue
			}
			if _, active := remaining[process.ProcessID]; active {
				failures[index] = fmt.Errorf("native command process %q remains active", process.ProcessID)
			}
		}
	}
	return errors.Join(failures...)
}

func readCodexCommandProcesses(threadID string, client codexCommandClient) (map[string]codexCommandProcess, error) {
	processes := make(map[string]codexCommandProcess)
	seenCursors := make(map[string]struct{})
	var cursor *string
	for page := 0; page < codexCommandPageLimit; page++ {
		raw, err := client.ListCommandProcesses(threadID, cursor, codexCommandPageSize)
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

func terminateCodexCommandProcess(threadID string, process codexCommandProcess, client codexCommandClient) (bool, error) {
	raw, err := client.TerminateCommandProcess(threadID, process.ProcessID)
	if err != nil {
		return false, err
	}
	var response struct {
		Terminated *bool `json:"terminated"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return false, fmt.Errorf("read native command termination: %w", err)
	}
	if response.Terminated == nil {
		return false, errors.New("the native command termination omits its result")
	}
	return *response.Terminated, nil
}
