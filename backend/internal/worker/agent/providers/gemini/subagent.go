package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type geminiChildRecord struct {
	Session geminiSession
	Path    string
	Info    os.FileInfo
}

type geminiChildWrite struct {
	Key     string
	Source  leapmuxv1.MessageSource
	Content agent.MessageContent
	Span    agent.SpanInfo
}

// Gemini stores every descendant below its exact root UUID. Its native records
// state no direct nested parent, so every child belongs to that proven root.
func geminiChildRecords(query agent.StoredSessionQuery, rootSessionID string) (_ []geminiChildRecord, err error) {
	if !validGeminiSessionID(rootSessionID) {
		return nil, errors.New("the Gemini child owner is invalid")
	}
	if _, err := locateGeminiSession(query, rootSessionID); err != nil {
		return nil, err
	}
	project, err := geminiProjectDirectory(query)
	if err != nil {
		return nil, err
	}
	rootPath := geminiConfigRoot(query)
	parts, valid := geminiPathParts(rootPath, filepath.Join(project, rootSessionID))
	if !valid {
		return nil, errors.New("the Gemini child directory leaves its configuration directory")
	}
	root, err := sessionstore.OpenArchiveRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, parts...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, chain.Close()) }()
	directory, err := chain.Root().Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	_, hash := geminiProjectIdentity(query.WorkingDir)
	records := make([]geminiChildRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".jsonl")
		if _, err := uuid.Parse(id); err != nil || id == rootSessionID {
			continue
		}
		info, err := chain.Root().Lstat(entry.Name())
		if err != nil {
			continue
		}
		data, err := sessionstore.ReadRegularFile(chain.Root(), entry.Name(), geminiSessionReadLimit)
		if err != nil {
			continue
		}
		session, err := decodeGeminiSession(data)
		if err != nil || session.SessionID != id || session.ProjectHash != hash || session.Kind != "subagent" {
			continue
		}
		records = append(records, geminiChildRecord{Session: session, Path: filepath.Join(project, rootSessionID, entry.Name()), Info: info})
	}
	return records, nil
}

// geminiChildWrites selects fields through supplemental data. Each original
// message and tool record remains complete and contains no invented ACP bytes.
func geminiChildWrites(session geminiSession) ([]geminiChildWrite, error) {
	var writes []geminiChildWrite
	for _, message := range session.Messages {
		if message.ID == "" || len(message.Original) == 0 {
			return nil, errors.New("the Gemini child message has no native identity or original record")
		}
		source := leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT
		if message.Type == "user" {
			source = leapmuxv1.MessageSource_MESSAGE_SOURCE_USER
		}
		supplement, err := json.Marshal(map[string]any{contracts.GeminiSupplementMessagePart: contracts.GeminiMessagePartContent})
		if err != nil {
			return nil, err
		}
		writes = append(writes, geminiChildWrite{Key: message.ID + ":content", Source: source, Content: agent.MessageContent{Original: message.Original, Supplemental: supplement}})
		for index := range message.Thoughts {
			supplement, err := json.Marshal(map[string]any{contracts.GeminiSupplementMessagePart: contracts.GeminiMessagePartThought, contracts.GeminiSupplementMessagePartIndex: index})
			if err != nil {
				return nil, err
			}
			writes = append(writes, geminiChildWrite{Key: fmt.Sprintf("%s:thought:%d", message.ID, index), Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: message.Original, Supplemental: supplement}})
		}
		for _, record := range message.ToolCalls {
			var tool geminiToolIdentity
			if json.Unmarshal(record, &tool) != nil || tool.ID == "" || tool.Name == "" || !strings.HasPrefix(tool.ID, tool.Name+"__") || (tool.Status != "success" && tool.Status != "error") {
				continue
			}
			writes = append(writes, geminiChildWrite{Key: tool.ID + ":result", Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: agent.MessageContent{Original: record}, Span: agent.SpanInfo{SpanID: tool.ID, SpanType: tool.Name, Closing: true}})
		}
	}
	return writes, nil
}
