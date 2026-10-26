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

// A root history contains every turn. Cap the total bytes read across its files
// at 64 MiB so a damaged archive cannot allocate without a limit.
const fastagentRootHistoryLimit = 64 << 20

type fastagentRootHistory struct {
	file     string
	messages []fastagentHistoryMessage
}

func readFastagentRootArchive(home, sessionID, workingDir string) (histories []fastagentRootHistory, err error) {
	if home == "" || !safeFastagentID(sessionID) || workingDir == "" {
		return nil, errors.New("the fastagent root archive identity is incomplete")
	}
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return nil, fmt.Errorf("open fastagent home: %w", err)
	}
	return readFastagentRootArchiveFromRoot(root, sessionID, workingDir)
}

func readFastagentRootArchiveFromRoot(root sessionstore.ArchiveRoot, sessionID, workingDir string) (histories []fastagentRootHistory, err error) {
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			histories, err = nil, errors.Join(err, closeErr)
		}
	}()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "sessions", sessionID)
	if err != nil {
		return nil, fmt.Errorf("open fastagent root session: %w", err)
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			histories, err = nil, errors.Join(err, closeErr)
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
	if snapshot.SessionID != sessionID || snapshot.Execution.ChildLink != nil {
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
	var files []string
	for _, actor := range snapshot.Continuation.Agents {
		file := actor.HistoryFile
		if !safeFastagentID(file) || !strings.HasPrefix(file, "history_") || !strings.HasSuffix(file, ".json") {
			return nil, errors.New("the fastagent root history path is invalid")
		}
		files = append(files, file)
	}
	slices.Sort(files)
	files = slices.Compact(files)
	if len(files) == 0 {
		return nil, errors.New("the fastagent root session has no history")
	}
	remaining := int64(fastagentRootHistoryLimit)
	for _, file := range files {
		raw, err := readFastagentRegularFile(directory, file, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(raw))
		var history struct {
			Messages *[]fastagentHistoryMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &history); err != nil {
			return nil, fmt.Errorf("decode fastagent root history: %w", err)
		}
		if history.Messages == nil {
			return nil, errors.New("the fastagent root history has no message array")
		}
		histories = append(histories, fastagentRootHistory{file: file, messages: *history.Messages})
	}
	return histories, nil
}
