package junie

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

type junieToolSource struct {
	tooltranscript.SourceDefaults
	home       string
	workingDir string
}

func newJunieToolTranscript(ctx context.Context, sink agent.ProviderServices, home, workingDir string) *tooltranscript.Transcript {
	return tooltranscript.New(ctx, sink, &junieToolSource{home: home, workingDir: workingDir})
}

func (*junieToolSource) ProviderName() string { return "Junie" }

func (s *junieToolSource) Locate(sessionID string) tooltranscript.Location {
	return tooltranscript.Location{SessionKey: sessionID, Path: filepath.Join(s.home, "sessions", sessionID), Ready: safeJunieSessionID(sessionID) && s.home != ""}
}

func (*junieToolSource) ToolCallID(original []byte) string {
	frame, ok := junieCompletedExecute(original)
	if !ok {
		return ""
	}
	return frame.CallID
}

func (s *junieToolSource) InitialSupplement(ctx context.Context, path string, original []byte, span agent.SpanInfo) ([]byte, error) {
	id := s.ToolCallID(original)
	if !span.Closing || id == "" || id != span.SpanID {
		return nil, nil
	}
	sessionID := filepath.Base(path)
	if !safeJunieSessionID(sessionID) || path != filepath.Join(s.home, "sessions", sessionID) {
		return nil, errors.New("the Junie output path location has another native session")
	}
	return readJunieOutputPath(ctx, s.home, s.workingDir, sessionID, original)
}

func (s *junieToolSource) ReadSupplements(ctx context.Context, location tooltranscript.Location, pending map[string]agent.MessageContent, _ bool) (map[string][]byte, error) {
	return readJunieToolSupplements(ctx, s.home, s.workingDir, location.Path, pending)
}

func (s *junieToolSource) CaptureSupplement(location tooltranscript.Location, content agent.MessageContent, _ agent.SpanInfo) tooltranscript.SupplementReader {
	id := s.ToolCallID(content.Original)
	if id == "" {
		return nil
	}
	home, workingDir, path := s.home, s.workingDir, location.Path
	pending := map[string]agent.MessageContent{id: content.Clone()}
	return func(ctx context.Context, _ bool) ([]byte, error) {
		records, err := readJunieToolSupplements(ctx, home, workingDir, path, pending)
		return records[id], err
	}
}

func readJunieToolSupplements(ctx context.Context, home, workingDir, path string, pending map[string]agent.MessageContent) (map[string][]byte, error) {
	result := make(map[string][]byte, len(pending))
	var failures error
	sessionID := filepath.Base(path)
	if !safeJunieSessionID(sessionID) || path != filepath.Join(home, "sessions", sessionID) {
		return nil, errors.New("the Junie output path location has another native session")
	}
	for id, content := range pending {
		frame, ok := junieCompletedExecute(content.Original)
		if !ok || id != frame.CallID {
			continue
		}
		if content.AgentSessionID != "" && content.AgentSessionID != sessionID {
			failures = errors.Join(failures, errors.New("the Junie output path row has another native session"))
			continue
		}
		extra, err := readJunieOutputPath(ctx, home, workingDir, sessionID, content.Original)
		failures = errors.Join(failures, err)
		if err == nil && len(extra) > 0 {
			result[id] = extra
		}
	}
	return result, failures
}
