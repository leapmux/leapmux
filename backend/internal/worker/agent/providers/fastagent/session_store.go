package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// A root history contains every turn. Cap the total bytes read across its files
// at 64 MiB so a damaged archive cannot allocate without a limit.
const fastagentRootHistoryLimit = 64 << 20
const fastagentRootElementLimit = 100000

type fastagentRootMessages []json.RawMessage

// Decode the array directly instead of copying it into an intermediate blob.
func (messages *fastagentRootMessages) UnmarshalJSON(raw []byte) error {
	decoded, err := fastagentArchiveArray(raw)
	if err != nil {
		return err
	}
	*messages = decoded
	return nil
}

type fastagentRootHistory struct {
	actor, file string
	messages    []json.RawMessage
}

type fastagentRootArchive struct {
	sessionID, activeActor string
	histories              []fastagentRootHistory
}

func validFastagentHistoryFile(file string) bool {
	return safeFastagentID(file) && strings.HasPrefix(file, "history_") && strings.HasSuffix(file, ".json")
}

func readFastagentRootArchive(home, sessionID, workingDir string) (*fastagentRootArchive, error) {
	if home == "" || !safeFastagentID(sessionID) || workingDir == "" {
		return nil, errors.New("the fastagent root archive identity is incomplete")
	}
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return nil, fmt.Errorf("open fastagent home: %w", err)
	}
	return readFastagentRootArchiveFromRoot(root, sessionID, workingDir)
}

func readFastagentRootArchiveFromRoot(root sessionstore.ArchiveRoot, sessionID, workingDir string) (archive *fastagentRootArchive, err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "sessions", sessionID)
	if err != nil {
		return nil, fmt.Errorf("open fastagent root session: %w", err)
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			archive, err = nil, errors.Join(err, closeErr)
		}
	}()
	directory := chain.Root()
	raw, err := readFastagentRegularFile(directory, "session.json", fastagentChildSnapshotLimit)
	if err != nil {
		return nil, err
	}
	var snapshot fastagentSessionSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("decode fastagent root session: %w", err)
	}
	active := snapshot.Continuation.ActiveAgent
	if snapshot.SessionID != sessionID || snapshot.Execution.ChildLink != nil || active == "" {
		return nil, errors.New("the fastagent root session identity is invalid")
	}
	expected, err := os.Stat(workingDir)
	if err != nil {
		return nil, fmt.Errorf("resolve fastagent working directory: %w", err)
	}
	actual, err := os.Stat(snapshot.Continuation.CWD)
	if err != nil || !filepath.IsAbs(workingDir) || !filepath.IsAbs(snapshot.Continuation.CWD) ||
		!expected.IsDir() || !actual.IsDir() || !os.SameFile(expected, actual) {
		return nil, errors.New("the fastagent root archive has a different working directory")
	}
	entry, exists := snapshot.Continuation.Agents[active]
	if !exists || !validFastagentHistoryFile(entry.HistoryFile) {
		return nil, errors.New("the Fast Agent active actor has no valid history")
	}
	actors := make([]string, 0, len(snapshot.Continuation.Agents))
	for actor := range snapshot.Continuation.Agents {
		actors = append(actors, actor)
	}
	// Sort checked reads only. Native history order determines transcript order.
	slices.Sort(actors)
	archive = &fastagentRootArchive{sessionID: sessionID, activeActor: active}
	owners := make(map[string]bool)
	remaining := int64(fastagentRootHistoryLimit)
	for _, actor := range actors {
		file := snapshot.Continuation.Agents[actor].HistoryFile
		if file == "" && actor != active {
			continue
		}
		if actor == "" || !validFastagentHistoryFile(file) {
			return nil, errors.New("the Fast Agent root history path is invalid")
		}
		if owners[file] {
			return nil, errors.New("the Fast Agent history has multiple actor owners")
		}
		owners[file] = true
		raw, err := readFastagentRegularFile(directory, file, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(raw))
		var history struct {
			Messages fastagentRootMessages `json:"messages"`
		}
		if err := json.Unmarshal(raw, &history); err != nil {
			return nil, fmt.Errorf("decode fastagent root history: %w", err)
		}
		if history.Messages == nil {
			return nil, errors.New("the Fast Agent root history has no message array")
		}
		archive.histories = append(archive.histories, fastagentRootHistory{actor: actor, file: file, messages: history.Messages})
	}
	after, err := readFastagentRegularFile(directory, "session.json", fastagentChildSnapshotLimit)
	if err != nil {
		return nil, err
	}
	var beforeJSON, afterJSON bytes.Buffer
	if err := json.Compact(&beforeJSON, raw); err != nil {
		return nil, err
	}
	if err := json.Compact(&afterJSON, after); err != nil {
		return nil, err
	}
	if !bytes.Equal(beforeJSON.Bytes(), afterJSON.Bytes()) {
		return nil, errors.New("the Fast Agent ownership changed during its archive read")
	}
	return archive, nil
}
