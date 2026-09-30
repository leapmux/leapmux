package fastagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	fastagentChildSnapshotLimit = 1 << 20
	fastagentChildHistoryLimit  = 16 << 20
)

type fastagentChildSnapshot struct {
	SessionID string `json:"session_id"`
	Metadata  struct {
		Extras struct {
			Label       string `json:"subagent_label"`
			TaskPreview string `json:"subagent_task_preview"`
			Ordinal     int    `json:"subagent_ordinal"`
		} `json:"extras"`
	} `json:"metadata"`
	Execution struct {
		Resumable bool `json:"resumable"`
		ChildLink *struct {
			ParentSessionID  string `json:"parent_session_id"`
			ParentToolCallID string `json:"parent_tool_call_id"`
		} `json:"child_link"`
	} `json:"execution"`
	Continuation struct {
		ActiveAgent string `json:"active_agent"`
		Agents      map[string]struct {
			HistoryFile string `json:"history_file"`
		} `json:"agents"`
	} `json:"continuation"`
}

// fastagentHistoryMessage preserves the order of native content and tool maps.
type fastagentHistoryMessage struct {
	Role        string                       `json:"role"`
	Content     []json.RawMessage            `json:"content"`
	ToolCalls   json.RawMessage              `json:"tool_calls"`
	ToolResults json.RawMessage              `json:"tool_results"`
	Channels    map[string][]json.RawMessage `json:"channels"`
}

type fastagentArchiveCandidate struct {
	childID, modelCallID string
	directory            string
	directoryInfo        os.FileInfo
	historyFile          string
	resolvedLabel        string
	ordinal              int
}

type fastagentParentResultIdentity struct {
	childID, modelCallID string
	requestedLabel       string
	resolvedLabel        string
	text                 string
}

type fastagentChildArchive struct {
	childID  string
	messages []fastagentHistoryMessage
}

// readFastagentChildArchive selects one stored child by its native backlink
// and its full input. The ACP call id differs from the model's tool use id.
func readFastagentChildArchive(home, parentSessionID string, child *fastagentChildState) (archive *fastagentChildArchive, err error) {
	if home == "" || !safeFastagentID(parentSessionID) || child.prompt == "" {
		return nil, errors.New("the fastagent child archive identity is incomplete")
	}
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return nil, fmt.Errorf("open fastagent home: %w", err)
	}
	return readFastagentChildArchiveFromRoot(root, parentSessionID, child)
}

func readFastagentChildArchiveFromRoot(root sessionstore.ArchiveRoot, parentSessionID string, child *fastagentChildState) (archive *fastagentChildArchive, err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	parentChain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "sessions", parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("open fastagent parent session: %w", err)
	}
	defer func() {
		if closeErr := parentChain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	parentRoot := parentChain.Root()
	childrenChain, err := sessionstore.OpenCheckedArchiveDirectoryChain(parentRoot, "children")
	if err != nil {
		return nil, fmt.Errorf("open fastagent child sessions: %w", err)
	}
	defer func() {
		if closeErr := childrenChain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	childrenRoot := childrenChain.Root()
	children, err := childrenRoot.Open(".")
	if err != nil {
		return nil, fmt.Errorf("read fastagent child sessions: %w", err)
	}
	defer func() {
		if closeErr := children.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	entries, err := children.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("list fastagent child sessions: %w", err)
	}
	var candidates []fastagentArchiveCandidate
	for _, entry := range entries {
		if !entry.IsDir() || !safeFastagentID(entry.Name()) {
			continue
		}
		snapshotRaw, directoryInfo, err := readFastagentCandidateSnapshot(childrenRoot, entry)
		if err != nil {
			continue
		}
		var snapshot fastagentChildSnapshot
		if json.Unmarshal(snapshotRaw, &snapshot) != nil || snapshot.Execution.ChildLink == nil {
			continue
		}
		link := snapshot.Execution.ChildLink
		if snapshot.Execution.Resumable || snapshot.SessionID != entry.Name() ||
			link.ParentSessionID != parentSessionID || link.ParentToolCallID == "" ||
			snapshot.Metadata.Extras.TaskPreview != fastagentTaskPreview(child.prompt) ||
			(!child.resultSeen && child.requestedLabel != "" && snapshot.Metadata.Extras.Label != child.requestedLabel) {
			continue
		}
		active := snapshot.Continuation.ActiveAgent
		historyFile := snapshot.Continuation.Agents[active].HistoryFile
		candidates = append(candidates, fastagentArchiveCandidate{
			childID: entry.Name(), modelCallID: link.ParentToolCallID,
			directory: entry.Name(), directoryInfo: directoryInfo, historyFile: historyFile,
			resolvedLabel: snapshot.Metadata.Extras.Label, ordinal: snapshot.Metadata.Extras.Ordinal,
		})
	}
	if len(candidates) == 0 {
		return nil, errors.New("no fastagent child matches the parent session and prompt")
	}
	if child.resultSeen {
		identity, err := fastagentChildForParentResult(parentRoot, parentSessionID, child.resultText, candidates)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			if candidate.childID != identity.childID || candidate.modelCallID != identity.modelCallID {
				continue
			}
			if identity.requestedLabel != child.requestedLabel {
				return nil, errors.New("the fastagent parent result has a different requested label")
			}
			if identity.resolvedLabel == "" || identity.resolvedLabel != candidate.resolvedLabel {
				return nil, errors.New("the fastagent parent result has a different resolved label")
			}
			messages, matchesPrompt, err := readFastagentCandidateHistory(childrenRoot, candidate, child.prompt)
			if err != nil {
				return nil, err
			}
			if !matchesPrompt {
				return nil, errors.New("the fastagent child archive prompt differs from its parent result")
			}
			return &fastagentChildArchive{childID: candidate.childID, messages: messages}, nil
		}
		return nil, errors.New("the fastagent parent result does not match a child archive backlink")
	}
	var matched *fastagentChildArchive
	for _, candidate := range candidates {
		messages, matchesPrompt, err := readFastagentCandidateHistory(childrenRoot, candidate, child.prompt)
		if err != nil {
			return nil, err
		}
		if !matchesPrompt {
			continue
		}
		if matched != nil {
			return nil, errors.New("multiple fastagent children match one ACP subagent")
		}
		matched = &fastagentChildArchive{childID: candidate.childID, messages: messages}
	}
	if matched == nil {
		return nil, errors.New("no fastagent child matches the parent session and prompt")
	}
	return matched, nil
}

func readFastagentCandidateSnapshot(childrenRoot sessionstore.ArchiveRoot, entry os.DirEntry) (raw []byte, info os.FileInfo, err error) {
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(childrenRoot, entry.Name())
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			raw, info, err = nil, nil, errors.Join(err, closeErr)
		}
	}()
	listed, err := entry.Info()
	if err != nil {
		return nil, nil, err
	}
	info, err = chain.Root().Stat(".")
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(listed, info) {
		return nil, nil, errors.New("the fastagent child directory changed after its listing")
	}
	raw, err = readFastagentRegularFile(chain.Root(), "session.json", fastagentChildSnapshotLimit)
	return raw, info, err
}

func readFastagentCandidateHistory(childrenRoot sessionstore.ArchiveRoot, candidate fastagentArchiveCandidate, prompt string) (messages []fastagentHistoryMessage, matches bool, err error) {
	if !safeFastagentID(candidate.historyFile) || !strings.HasPrefix(candidate.historyFile, "history_") ||
		!strings.HasSuffix(candidate.historyFile, ".json") {
		return nil, false, errors.New("the fastagent child history path is invalid")
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(childrenRoot, candidate.directory)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			messages, matches, err = nil, false, errors.Join(err, closeErr)
		}
	}()
	currentInfo, err := chain.Root().Stat(".")
	if err != nil {
		return nil, false, err
	}
	if !os.SameFile(candidate.directoryInfo, currentInfo) {
		return nil, false, errors.New("the fastagent child directory changed before its history read")
	}
	historyRaw, err := readFastagentRegularFile(chain.Root(), candidate.historyFile, fastagentChildHistoryLimit)
	if err != nil {
		return nil, false, err
	}
	var history struct {
		Messages []fastagentHistoryMessage `json:"messages"`
	}
	if json.Unmarshal(historyRaw, &history) != nil || len(history.Messages) == 0 {
		return nil, false, errors.New("the fastagent child history is empty or unreadable")
	}
	return history.Messages, fastagentHistoryPromptMatches(history.Messages, prompt), nil
}

// fastagentChildForParentResult links ACP's final result text to the model call
// and child session that the parent history stores. ACP uses another call ID.
func fastagentChildForParentResult(root sessionstore.ArchiveRoot, parentSessionID, resultText string, candidates []fastagentArchiveCandidate) (*fastagentParentResultIdentity, error) {
	snapshotRaw, err := readFastagentRegularFile(root, "session.json", fastagentChildSnapshotLimit)
	if err != nil {
		return nil, fmt.Errorf("read fastagent parent session: %w", err)
	}
	var snapshot struct {
		SessionID    string `json:"session_id"`
		Continuation struct {
			Agents map[string]struct {
				HistoryFile string `json:"history_file"`
			} `json:"agents"`
		} `json:"continuation"`
	}
	if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil || snapshot.SessionID != parentSessionID {
		return nil, errors.New("the fastagent parent session identity is invalid")
	}
	files := make([]string, 0, len(snapshot.Continuation.Agents))
	for _, actor := range snapshot.Continuation.Agents {
		if !safeFastagentID(actor.HistoryFile) || !strings.HasPrefix(actor.HistoryFile, "history_") ||
			!strings.HasSuffix(actor.HistoryFile, ".json") {
			return nil, errors.New("the fastagent parent history path is invalid")
		}
		files = append(files, actor.HistoryFile)
	}
	slices.Sort(files)
	files = slices.Compact(files)
	if len(files) == 0 {
		return nil, errors.New("the fastagent parent session has no history")
	}
	type resultKey struct{ childID, modelCallID string }
	var matched *fastagentParentResultIdentity
	seenResults := make(map[resultKey]fastagentParentResultIdentity)
	for _, filename := range files {
		raw, err := readFastagentRegularFile(root, filename, fastagentChildHistoryLimit)
		if err != nil {
			return nil, fmt.Errorf("read fastagent parent history: %w", err)
		}
		var history struct {
			Messages []fastagentHistoryMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &history); err != nil {
			return nil, fmt.Errorf("decode fastagent parent history: %w", err)
		}
		for _, message := range history.Messages {
			entries, err := fastagentObjectEntries(message.ToolResults)
			if err != nil {
				return nil, fmt.Errorf("decode fastagent parent tool results: %w", err)
			}
			for _, entry := range entries {
				var result struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					Meta map[string]json.RawMessage `json:"_meta"`
				}
				if err := json.Unmarshal(entry.Data, &result); err != nil {
					return nil, fmt.Errorf("decode fastagent parent tool result: %w", err)
				}
				metaRaw, ok := result.Meta["fast-agent-subagent"]
				if !ok {
					continue
				}
				var meta struct {
					ChildSessionID   string  `json:"child_session_id"`
					ParentToolCallID string  `json:"parent_tool_call_id"`
					RequestedLabel   *string `json:"requested_label"`
					Label            string  `json:"label"`
				}
				if err := json.Unmarshal(metaRaw, &meta); err != nil {
					return nil, fmt.Errorf("decode fastagent subagent result identity: %w", err)
				}
				if meta.ChildSessionID == "" || meta.ParentToolCallID != entry.ID {
					continue
				}
				var parts []string
				for _, content := range result.Content {
					if content.Type == "text" {
						parts = append(parts, content.Text)
					}
				}
				text := strings.Join(parts, "\n")
				requestedLabel := ""
				if meta.RequestedLabel != nil {
					requestedLabel = *meta.RequestedLabel
				}
				identity := fastagentParentResultIdentity{
					childID: meta.ChildSessionID, modelCallID: entry.ID, text: text,
					requestedLabel: requestedLabel, resolvedLabel: meta.Label,
				}
				key := resultKey{childID: meta.ChildSessionID, modelCallID: entry.ID}
				if prior, exists := seenResults[key]; exists && prior != identity {
					return nil, errors.New("the fastagent parent history gives one child conflicting results")
				}
				seenResults[key] = identity
				if text != resultText {
					continue
				}
				if matched != nil && (matched.childID != identity.childID || matched.modelCallID != identity.modelCallID) {
					return nil, errors.New("multiple fastagent parent results match one ACP subagent")
				}
				matched = &identity
			}
		}
	}
	selectedOrdinal := 0
	selected := false
	for _, candidate := range candidates {
		if matched != nil && candidate.childID == matched.childID && candidate.modelCallID == matched.modelCallID {
			selectedOrdinal = candidate.ordinal
			selected = true
		}
	}
	for _, candidate := range candidates {
		key := resultKey{childID: candidate.childID, modelCallID: candidate.modelCallID}
		if _, exists := seenResults[key]; !exists {
			for seen := range seenResults {
				if seen.childID == candidate.childID {
					return nil, errors.New("the fastagent parent result does not match a child archive backlink")
				}
			}
			if selected && selectedOrdinal > 0 && candidate.ordinal > 0 && candidate.ordinal < selectedOrdinal {
				continue
			}
			return nil, errors.New("the fastagent parent history lacks a result for a matching child archive")
		}
	}
	if matched == nil {
		return nil, errors.New("no fastagent parent result matches the ACP subagent")
	}
	if !selected {
		return nil, errors.New("the fastagent parent result does not match a child archive backlink")
	}
	return matched, nil
}

func fastagentTaskPreview(prompt string) string {
	value := []rune(strings.Join(strings.Fields(prompt), " "))
	if len(value) > 80 {
		value = value[:80]
	}
	return string(value)
}

func fastagentHistoryPromptMatches(messages []fastagentHistoryMessage, prompt string) bool {
	for _, message := range messages {
		if message.Role != "user" || len(message.Content) == 0 {
			continue
		}
		var first struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content[0], &first) != nil || first.Type != "text" {
			return false
		}
		return first.Text == prompt || strings.HasPrefix(first.Text, prompt+"\n\n<included_user_context>")
	}
	return false
}

func safeFastagentID(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!strings.ContainsAny(value, `/\`)
}

type fastagentArchiveReadRoot interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (*os.File, error)
}

func readFastagentRegularFile(root fastagentArchiveReadRoot, path string, limit int64) ([]byte, error) {
	data, err := sessionstore.ReadRegularFile(root, path, limit)
	if err != nil {
		return nil, fmt.Errorf("read the fastagent archive file: %w", err)
	}
	return data, nil
}
