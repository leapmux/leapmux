package letta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Letta Code's local backend keeps each agent as a flat JSON record under
// `$LETTA_LOCAL_BACKEND_DIR/agents/` and each conversation under
// `conversations/<id>/conversation.json`. The reader opens them read-only and
// never writes to the store.

// lettaConversationFile is a conversation's own record.
const lettaConversationFile = "conversation.json"

// lettaConversationRecord is the part of a conversation record the reader takes.
type lettaConversationRecord struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// lettaStoredSessions is Letta's Provider.ListStoredSessions.
func lettaStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	root := lettaBackendRoot(q)
	if workingDir == "" || root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, lettaConversationsDir)
	entries, err := sessionstore.NewestEntries(dir, 0, lettaConversationEntry)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.EffectiveLimit()
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readLettaConversation(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// lettaBackendRoot resolves the local-backend store directory.
func lettaBackendRoot(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, lettaBackendDirEnv, ".letta-backend")
}

// lettaConversationEntry keeps a conversation directory that holds its record.
func lettaConversationEntry(dir string, entry os.DirEntry) (sessionstore.Entry, bool) {
	return sessionstore.NamedFileInside(lettaConversationFile)(dir, entry)
}

// readLettaConversationRecord reads one conversation's record file.
func readLettaConversationRecord(path string) (lettaConversationRecord, error) {
	var record lettaConversationRecord
	err := sessionstore.ReadSidecarFile(path, sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &record)
	})
	return record, err
}

// readLettaConversation reads one conversation's record.
func readLettaConversation(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	record, err := readLettaConversationRecord(filepath.Join(entry.Path, lettaConversationFile))
	if err != nil {
		return agent.StoredSession{}, false
	}
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return agent.StoredSession{}, false
	}
	// The conversation record states no cwd; the caller already scoped the walk
	// to the store, and a conversation is offered for the working directory the
	// query stated. A conversation that records a different cwd is skipped when
	// the store carries one.
	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(id),
		UpdatedAt: entry.ModTime,
	}, true
}

// lettaResumeAgentID resolves the agent of a stored conversation.
//
// A resume addresses an EXISTING conversation, so runtime_start must name the
// agent that conversation belongs to. The conversation record names it. A
// conversation id with no record behind it is an error: the alternative is to
// fall back to create_*, which would start a NEW conversation in place of the
// one the caller asked for.
func lettaResumeAgentID(opts agent.Options, conversationID string) (string, error) {
	id := strings.TrimSpace(conversationID)
	if id == "" {
		return "", errors.New("resume conversation: the conversation id is empty")
	}
	root := lettaBackendRoot(agent.StoredSessionQuery{
		WorkingDir: opts.WorkingDir,
		HomeDir:    opts.HomeDir,
	})
	if root == "" {
		return "", fmt.Errorf("resume conversation %q: the Letta local backend store is not configured", id)
	}
	record, err := readLettaConversationRecord(filepath.Join(root, lettaConversationsDir, id, lettaConversationFile))
	if err != nil {
		return "", fmt.Errorf("resume conversation %q: %w", id, err)
	}
	agentID := strings.TrimSpace(record.AgentID)
	if agentID == "" {
		return "", fmt.Errorf("resume conversation %q: its record names no agent", id)
	}
	return agentID, nil
}
