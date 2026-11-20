package dirac

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

type diracTaskletOutcome struct {
	Kind          string   `json:"kind"`
	Response      *string  `json:"response"`
	CompletedAt   *float64 `json:"completedAt"`
	FailedAt      *float64 `json:"failedAt"`
	CancelledAt   *float64 `json:"cancelledAt"`
	InterruptedAt *float64 `json:"interruptedAt"`
	Reason        *string  `json:"reason"`
	Error         *struct {
		Name    *string `json:"name"`
		Message *string `json:"message"`
	} `json:"error"`
}

type diracTaskletRecord struct {
	ID             string               `json:"id"`
	RootTaskID     string               `json:"rootTaskId"`
	ConversationID string               `json:"conversationUlid"`
	Title          *string              `json:"title"`
	Prompt         *string              `json:"prompt"`
	Role           string               `json:"role"`
	CreatedAt      *float64             `json:"createdAt"`
	NativeOutcome  json.RawMessage      `json:"outcome"`
	Outcome        *diracTaskletOutcome `json:"-"`
	lifecycle      diracTaskletLifecycle
}

type diracStoredTasklet struct {
	SpawnCardID string
	Record      diracTaskletRecord
}

type diracTaskletMetadata struct {
	RootTaskID string
	Tasklets   []diracStoredTasklet
	Cards      map[string]bool
}

type diracSpawnBinding struct {
	cardID string
	title  *string
	prompt *string
}

// readDiracTaskletMetadata reads only the current session's root-qualified
// native records. It creates, repairs and changes no native file.
func readDiracTaskletMetadata(profile, sessionID string) (metadata *diracTaskletMetadata, err error) {
	if !safeDiracComponent(sessionID) {
		return nil, fmt.Errorf("the Dirac tasklet session ID is invalid")
	}
	root, err := sessionstore.OpenArchiveRoot(profile)
	if err != nil {
		return nil, err
	}
	return readDiracTaskletMetadataFromRoot(root, sessionID)
}

func readDiracTaskletMetadataFromRoot(root sessionstore.ArchiveRoot, sessionID string) (metadata *diracTaskletMetadata, err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			metadata, err = nil, errors.Join(err, closeErr)
		}
	}()
	taskID, err := readDiracRuntimeTask(root, sessionID)
	if err != nil {
		return nil, err
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "data", "tasks", taskID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			metadata, err = nil, errors.Join(err, closeErr)
		}
	}()
	messages, err := readDiracPresentation(chain.Root())
	if err != nil {
		return nil, err
	}
	bindings, order, err := diracSpawnBindings(messages)
	if err != nil {
		return nil, err
	}
	cards, err := diracTaskletCardStates(messages)
	if err != nil {
		return nil, err
	}
	metadata = &diracTaskletMetadata{RootTaskID: taskID, Tasklets: make([]diracStoredTasklet, 0, len(order)), Cards: cards}
	conversations := make(map[string]string, len(order))
	for _, id := range order {
		record, err := readDiracTaskletRecord(chain.Root(), taskID, id)
		if err != nil {
			return nil, err
		}
		if record.ConversationID == sessionID {
			return nil, fmt.Errorf("the Dirac tasklet identifies its root as its child conversation")
		}
		binding := bindings[id]
		if binding.title != nil && *binding.title != *record.Title || binding.prompt != nil && *binding.prompt != *record.Prompt {
			return nil, fmt.Errorf("the Dirac tasklet assignment differs from its native spawn receipt")
		}
		if previous, exists := conversations[record.ConversationID]; exists && previous != id {
			return nil, fmt.Errorf("two Dirac tasklets declare the same native conversation")
		}
		conversations[record.ConversationID] = id
		metadata.Tasklets = append(metadata.Tasklets, diracStoredTasklet{SpawnCardID: binding.cardID, Record: record})
	}
	currentTask, err := readDiracRuntimeTask(root, sessionID)
	if err != nil {
		return nil, err
	}
	if currentTask != taskID {
		return nil, fmt.Errorf("the Dirac session changed its native root during the tasklet read")
	}
	return metadata, nil
}

func diracTaskletCardStates(messages []json.RawMessage) (map[string]bool, error) {
	cards := make(map[string]bool)
	for _, raw := range messages {
		var message struct {
			ID      string `json:"id"`
			Content struct {
				Type string `json:"type"`
				Card struct {
					ID     string `json:"id"`
					Tool   string `json:"toolName"`
					Status string `json:"status"`
				} `json:"card"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		card := message.Content.Card
		if message.Content.Type != "card" || card.Tool != contracts.DiracToolSubagents {
			continue
		}
		if card.ID == "" || card.ID != message.ID {
			return nil, fmt.Errorf("the Dirac tasklet card has another presentation identity")
		}
		if _, exists := cards[card.ID]; exists {
			return nil, fmt.Errorf("the Dirac presentation repeats a tasklet card identity")
		}
		switch card.Status {
		case "success", "error", "skipped", "cancelled", "abandoned":
			cards[card.ID] = true
		default:
			cards[card.ID] = false
		}
	}
	return cards, nil
}

func readDiracRuntimeTask(root sessionstore.ArchiveRoot, sessionID string) (string, error) {
	link, err := readDiracRuntimeLink(root, sessionID)
	if err != nil {
		return "", err
	}
	return link.TaskID, nil
}

// readDiracRuntimeLink reads the runtime-link record that binds one ACP session
// to its native root task and runtime mode.
func readDiracRuntimeLink(root sessionstore.ArchiveRoot, sessionID string) (diracRuntimeLink, error) {
	raw, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, diracTaskHistoryLimit, "data", "acp-session-runtime-configs", sessionID+".json")
	if err != nil {
		return diracRuntimeLink{}, err
	}
	return decodeDiracRuntimeLink(raw)
}

func readDiracPresentation(root sessionstore.ArchiveRoot) ([]json.RawMessage, error) {
	baseline, err := readOptionalDiracFile(root, "ui_messages.baseline.jsonl")
	if err != nil {
		return nil, err
	}
	var snapshot []byte
	if baseline == nil {
		snapshot, err = readOptionalDiracFile(root, "ui_messages.json")
		if err != nil {
			return nil, err
		}
	}
	operations, err := readOptionalDiracFile(root, "ui_messages.jsonl")
	if err != nil {
		return nil, err
	}
	return decodeDiracPresentation(snapshot, baseline, operations)
}

func readOptionalDiracFile(root sessionstore.ArchiveRoot, name string) ([]byte, error) {
	data, err := readDiracRegularFile(root, name, diracTranscriptLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func diracSpawnBindings(messages []json.RawMessage) (map[string]diracSpawnBinding, []string, error) {
	bindings := make(map[string]diracSpawnBinding)
	var order []string
	for _, raw := range messages {
		var message struct {
			Content struct {
				Type string `json:"type"`
				Card *struct {
					ID       string `json:"id"`
					ToolName string `json:"toolName"`
					Receipt  *struct {
						Operation string `json:"operation"`
						Rows      []struct {
							Operation string  `json:"operation"`
							TaskletID string  `json:"taskletId"`
							Title     *string `json:"title"`
							Message   *string `json:"message"`
						} `json:"rows"`
					} `json:"taskletReceipt"`
				} `json:"card"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, nil, err
		}
		card := message.Content.Card
		if message.Content.Type != "card" || card == nil || card.ToolName != contracts.DiracToolSubagents || card.Receipt == nil || card.Receipt.Operation != "spawn" {
			continue
		}
		if card.ID == "" || card.Receipt.Rows == nil {
			return nil, nil, fmt.Errorf("the Dirac spawn receipt has no card identity or row array")
		}
		for _, row := range card.Receipt.Rows {
			if row.Operation != "spawn" || row.TaskletID == "" {
				continue
			}
			if !validDiracTaskletID(row.TaskletID) {
				return nil, nil, fmt.Errorf("the Dirac spawn receipt has an invalid tasklet ID")
			}
			if _, exists := bindings[row.TaskletID]; exists {
				return nil, nil, fmt.Errorf("multiple Dirac spawn receipt rows identify one tasklet")
			}
			bindings[row.TaskletID] = diracSpawnBinding{cardID: card.ID, title: row.Title, prompt: row.Message}
			order = append(order, row.TaskletID)
		}
	}
	return bindings, order, nil
}

func readDiracTaskletRecord(root sessionstore.ArchiveRoot, taskID, id string) (record diracTaskletRecord, err error) {
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "tasklets", id)
	if err != nil {
		return record, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			record, err = diracTaskletRecord{}, errors.Join(err, closeErr)
		}
	}()
	raw, err := readDiracRegularFile(chain.Root(), "tasklet.json", diracTaskHistoryLimit)
	if err != nil {
		return record, err
	}
	record, err = decodeDiracTaskletRecord(raw, taskID, id)
	if err != nil {
		return record, err
	}
	events, err := readOptionalDiracFile(chain.Root(), "tasklet_events.json")
	if err != nil {
		return diracTaskletRecord{}, err
	}
	journal, err := decodeDiracTaskletJournal(events, id)
	if err != nil {
		return diracTaskletRecord{}, err
	}
	currentRaw, err := readDiracRegularFile(chain.Root(), "tasklet.json", diracTaskHistoryLimit)
	if err != nil {
		return diracTaskletRecord{}, err
	}
	current, err := decodeDiracTaskletRecord(currentRaw, taskID, id)
	if err != nil {
		return diracTaskletRecord{}, err
	}
	if current.ConversationID != record.ConversationID || *current.Title != *record.Title || *current.Prompt != *record.Prompt || !sameDiracNativeOutcome(current.NativeOutcome, record.NativeOutcome) {
		return diracTaskletRecord{}, fmt.Errorf("the Dirac tasklet changed its owner or outcome during the journal read")
	}
	record.lifecycle = journal.Lifecycle
	// Native savedSettledRecord requires the exact durable outcome event. The
	// native host can reuse an identical event across explicit resumptions.
	if record.Outcome != nil && !agent.JSONCanonicalEqual(record.NativeOutcome, journal.Outcome) {
		record.Outcome = nil
	}
	return record, nil
}

func sameDiracNativeOutcome(left, right json.RawMessage) bool {
	return len(left) == 0 && len(right) == 0 || agent.JSONCanonicalEqual(left, right)
}

func decodeDiracTaskletRecord(raw []byte, taskID, id string) (record diracTaskletRecord, err error) {
	if err := json.Unmarshal(raw, &record); err != nil {
		return diracTaskletRecord{}, err
	}
	if record.ID != id || record.RootTaskID != taskID || record.ConversationID == "" || record.Title == nil || record.Prompt == nil || !validDiracNativeTime(record.CreatedAt) {
		return diracTaskletRecord{}, fmt.Errorf("the Dirac tasklet record has another owner or an incomplete assignment")
	}
	if len(record.NativeOutcome) != 0 {
		if err := json.Unmarshal(record.NativeOutcome, &record.Outcome); err != nil || record.Outcome == nil || !record.Outcome.valid() {
			return diracTaskletRecord{}, fmt.Errorf("the Dirac tasklet record has an invalid outcome")
		}
	}
	return record, nil
}

func validDiracNativeTime(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func (outcome *diracTaskletOutcome) valid() bool {
	switch outcome.Kind {
	case "completed":
		return outcome.Response != nil && validDiracNativeTime(outcome.CompletedAt)
	case "failed":
		return outcome.Error != nil && outcome.Error.Name != nil && outcome.Error.Message != nil && validDiracNativeTime(outcome.FailedAt)
	case "cancelled":
		return validDiracNativeTime(outcome.CancelledAt)
	case "interrupted":
		return outcome.Reason != nil && validDiracNativeTime(outcome.InterruptedAt)
	default:
		return false
	}
}

func (outcome *diracTaskletOutcome) status() bgtask.Status {
	if outcome == nil {
		return bgtask.StatusRunning
	}
	switch outcome.Kind {
	case "completed":
		return bgtask.StatusSucceeded
	case "failed":
		return bgtask.StatusFailed
	default:
		return bgtask.StatusStopped
	}
}

func validDiracTaskletID(id string) bool {
	if !safeDiracComponent(id) || strings.TrimSpace(id) != id || strings.HasSuffix(id, ".") || strings.ContainsAny(id, `<>:"|?*`) {
		return false
	}
	units := 0
	for _, character := range id {
		if character < 32 || character == 127 {
			return false
		}
		units++
		if character > 0xffff {
			units++
		}
		if units > 128 {
			return false
		}
	}
	stem, _, _ := strings.Cut(strings.ToLower(id), ".")
	switch stem {
	case "con", "prn", "aux", "nul":
		return false
	}
	return len(stem) != 4 || (stem[:3] != "com" && stem[:3] != "lpt") || stem[3] < '1' || stem[3] > '9'
}
