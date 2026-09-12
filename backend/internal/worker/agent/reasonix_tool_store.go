package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var reasonixResultClip = regexp.MustCompile(`\n…\(([0-9]+) more chars truncated\)$`)

type reasonixToolRecord struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	RawContent string `json:"raw_content"`
}

func reasonixToolStorePath(sessionID, workingDir string) string {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	roots := reasonixSessionRoots(StoredSessionQuery{}, workingDir)
	for _, root := range roots {
		path := filepath.Join(root, sessionID+".jsonl")
		var meta struct {
			SessionID        string `json:"sessionId"`
			ActiveTranscript string `json:"activeTranscript"`
		}
		data, err := os.ReadFile(filepath.Join(root, sessionID+reasonixACPSuffix))
		if err == nil && json.Unmarshal(data, &meta) == nil && meta.SessionID == sessionID &&
			meta.ActiveTranscript != "" && filepath.Base(meta.ActiveTranscript) == meta.ActiveTranscript &&
			strings.HasSuffix(meta.ActiveTranscript, ".jsonl") {
			target := filepath.Join(root, meta.ActiveTranscript)
			targetData, err := os.ReadFile(strings.TrimSuffix(target, ".jsonl") + reasonixACPSuffix)
			var targetMeta reasonixACPMeta
			if err == nil && json.Unmarshal(targetData, &targetMeta) == nil && targetMeta.SessionID == sessionID {
				if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
					return target
				}
			}
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
		if info, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	if len(roots) == 0 {
		return ""
	}
	return filepath.Join(roots[len(roots)-1], sessionID+".jsonl")
}

func newReasonixToolTranscript(ctx context.Context, services ProviderServices, sessionID func() string, workingDir string) *toolTranscript {
	return &toolTranscript{
		ProviderServices: services,
		toolCallID:       acpToolCallID,
		ctx:              ctx,
		providerName:     "Reasonix",
		locate: func(_ string) toolTranscriptLocation {
			id := sessionID()
			return toolTranscriptLocation{sessionKey: id, path: reasonixToolStorePath(id, workingDir)}
		},
		readSupplements: func(ctx context.Context, path string, pending map[string]MessageContent, _ bool) (map[string][]byte, error) {
			return readReasonixToolSupplements(ctx, path, pending)
		},
	}
}

// reasonixResultMatches verifies that the saved result produced the protocol text.
func reasonixResultMatches(original, stored string) bool {
	if original == stored {
		return true
	}
	match := reasonixResultClip.FindStringSubmatchIndex(original)
	if match == nil {
		return false
	}
	omitted, err := strconv.Atoi(original[match[2]:match[3]])
	return err == nil && omitted > 0 && len(stored) >= match[0] &&
		len(stored)-match[0] == omitted && strings.HasPrefix(stored, original[:match[0]])
}

// ACP sends the first error line. The native transcript also retains the tool's details.
func reasonixStoredResultMatches(original, stored string, failed bool) bool {
	if reasonixResultMatches(original, stored) {
		return true
	}
	if !failed {
		return false
	}
	firstLine, _, hasDetails := strings.Cut(stored, "\n")
	headline, hasPrefix := strings.CutPrefix(firstLine, "error: ")
	return hasDetails && hasPrefix && reasonixResultMatches(original, headline)
}

func readReasonixToolSupplements(ctx context.Context, path string, pending map[string]MessageContent) (map[string][]byte, error) {
	resolved := make(map[string][]byte, len(pending))
	for id, content := range pending {
		resolved[id] = resolveACPMessageContent(content)
	}
	records, err := readReasonixToolRecords(ctx, path, resolved)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(records))
	for id, raw := range records {
		var record reasonixToolRecord
		if json.Unmarshal(raw, &record) != nil || record.Role != "tool" || record.ToolCallID != id {
			continue
		}
		var original struct {
			Status  string `json:"status"`
			Content []struct {
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(resolved[id], &original) != nil || len(original.Content) != 1 || original.Content[0].Content.Type != "text" {
			continue
		}
		text := original.Content[0].Content.Text
		failed := original.Status == "failed"
		if !reasonixStoredResultMatches(text, record.Content, failed) && (record.RawContent == "" || !reasonixStoredResultMatches(text, record.RawContent, failed)) {
			continue
		}
		var originalFields map[string]json.RawMessage
		if err := json.Unmarshal(pending[id].Original, &originalFields); err != nil {
			return nil, err
		}
		supplement := acpToolSupplement(originalFields)
		output, err := json.Marshal(map[string]json.RawMessage{"reasonix": raw})
		if err != nil {
			return nil, err
		}
		supplement["rawOutput"] = output
		encoded, err := json.Marshal(supplement)
		if err != nil {
			return nil, err
		}
		out[id] = encoded
	}
	return out, nil
}

// Read the native event log first. A checkpoint alone can describe an older branch.
func readReasonixToolRecords(ctx context.Context, path string, pending map[string][]byte) (records map[string]json.RawMessage, err error) {
	eventPath := strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"
	file, err := os.Open(eventPath)
	if err == nil {
		defer func() { err = errors.Join(err, file.Close()) }()
		return readReasonixToolEvents(ctx, file, pending)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err = os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	out := make(map[string]json.RawMessage)
	err = readReasonixJSONL(ctx, file, func(raw json.RawMessage) error {
		id, record := reasonixSelectedRecord(raw, pending)
		if id != "" {
			out[id] = record
		}
		return nil
	})
	return out, err
}

func reasonixSelectedRecord(raw json.RawMessage, pending map[string][]byte) (string, json.RawMessage) {
	var header struct {
		Role       string `json:"role"`
		ToolCallID string `json:"tool_call_id"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Role != "tool" {
		return "", nil
	}
	if _, found := pending[header.ToolCallID]; !found {
		return "", nil
	}
	return header.ToolCallID, raw
}

func readReasonixJSONL(ctx context.Context, file *os.File, visit func(json.RawMessage) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the Reasonix transcript is not a regular file")
	}
	// Read the captured size so a growing log cannot keep the read active indefinitely.
	scanner := newStdoutScanner(io.LimitReader(file, info.Size()))
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := scanner.Bytes()
		if !json.Valid(raw) {
			return fmt.Errorf("the Reasonix transcript contains invalid JSON")
		}
		if err := visit(append(json.RawMessage(nil), raw...)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}
