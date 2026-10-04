package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	geminiRegistryReadLimit = 8 << 20
	geminiSessionReadLimit  = 64 << 20
	geminiMarkerReadLimit   = 64 << 10
)

func geminiConfigRoot(query agent.StoredSessionQuery) string {
	home := query.Env("GEMINI_CLI_HOME")
	if home == "" {
		home = query.Home()
	}
	if home == "" {
		return ""
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(query.WorkingDir, home)
	}
	return filepath.Join(home, ".gemini")
}

func geminiProjectIdentity(workingDir string) (path, hash string) {
	if workingDir == "" || !filepath.IsAbs(workingDir) {
		return "", ""
	}
	path = filepath.Clean(workingDir)
	digest := sha256.Sum256([]byte(path))
	hash = hex.EncodeToString(digest[:])
	return path, hash
}

func geminiRegistryPath(workingDir string) string {
	path := filepath.Clean(workingDir)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// Gemini's project registry identifies a slug; its marker verifies the owner.
func geminiProjectDirectory(query agent.StoredSessionQuery) (directory string, err error) {
	workingDir, _ := geminiProjectIdentity(query.WorkingDir)
	rootPath := geminiConfigRoot(query)
	if workingDir == "" || rootPath == "" {
		return "", sessionstore.ErrAbsent
	}
	root, err := sessionstore.OpenArchiveRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	data, err := sessionstore.ReadRegularFile(root, "projects.json", geminiRegistryReadLimit)
	if err != nil {
		return "", err
	}
	var registry struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(data, &registry) != nil || registry.Projects == nil {
		return "", errors.New("the Gemini project registry is invalid")
	}
	slug := registry.Projects[geminiRegistryPath(workingDir)]
	if !validGeminiPathComponent(slug) {
		return "", sessionstore.ErrAbsent
	}
	marker, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, geminiMarkerReadLimit, "tmp", slug, ".project_root")
	if err != nil {
		return "", err
	}
	if geminiRegistryPath(strings.TrimSpace(string(marker))) != geminiRegistryPath(workingDir) {
		return "", errors.New("the Gemini project marker belongs to another directory")
	}
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "tmp", slug, "chats")
	if err != nil {
		return "", err
	}
	if err := chain.Close(); err != nil {
		return "", err
	}
	return filepath.Join(rootPath, "tmp", slug, "chats"), nil
}

func validGeminiPathComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\\x00")
}

func validGeminiSessionID(value string) bool {
	if !validGeminiPathComponent(value) {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < '0' || character > '9') && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

func geminiSessionEntries(directory string, limit int) ([]sessionstore.Entry, error) {
	return sessionstore.NewestEntries(directory, limit, sessionstore.EntryItself(func(entry os.DirEntry) bool {
		return !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasPrefix(entry.Name(), "session-") &&
			(filepath.Ext(entry.Name()) == ".jsonl" || filepath.Ext(entry.Name()) == ".json")
	}))
}

func readGeminiSession(query agent.StoredSessionQuery, path string) (session geminiSession, err error) {
	rootPath := geminiConfigRoot(query)
	relative, err := filepath.Rel(rootPath, path)
	if err != nil || rootPath == "" || !filepath.IsLocal(relative) {
		return session, errors.New("the Gemini session path is outside its configuration directory")
	}
	root, err := sessionstore.OpenArchiveRoot(rootPath)
	if err != nil {
		return session, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	data, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, geminiSessionReadLimit, strings.Split(relative, string(filepath.Separator))...)
	if err != nil {
		return session, err
	}
	return decodeGeminiSession(data)
}

func geminiStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	directory, err := geminiProjectDirectory(query)
	if err != nil {
		return nil, nil
	}
	_, hash := geminiProjectIdentity(query.WorkingDir)
	entries, err := geminiSessionEntries(directory, 0)
	if err != nil {
		return nil, nil
	}
	sessions := make([]agent.StoredSession, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, err := readGeminiSession(query, entry.Path)
		if err != nil || session.ProjectHash != hash || session.Kind == "subagent" || !validGeminiSessionID(session.SessionID) {
			continue
		}
		title := strings.TrimSpace(session.Summary)
		if title == "" {
			for _, message := range session.Messages {
				if message.Type != "user" {
					continue
				}
				candidate := strings.TrimSpace(geminiMessageText(message.Content))
				if candidate != "" && !strings.HasPrefix(candidate, "<session_context>") {
					title = candidate
					break
				}
			}
		}
		updated := session.LastUpdated
		if updated.IsZero() {
			updated = session.StartTime
		}
		sessions = append(sessions, agent.StoredSession{Handle: session.SessionID, Title: sessionstore.TrimTitle(title), UpdatedAt: updated})
	}
	return agent.SortAndCapSessions(sessions, query.EffectiveLimit()), nil
}

func locateGeminiSession(query agent.StoredSessionQuery, sessionID string) (string, error) {
	if !validGeminiSessionID(sessionID) {
		return "", errors.New("the Gemini session identifier is invalid")
	}
	directory, err := geminiProjectDirectory(query)
	if err != nil {
		return "", err
	}
	_, hash := geminiProjectIdentity(query.WorkingDir)
	entries, err := geminiSessionEntries(directory, 0)
	if err != nil {
		return "", err
	}
	shortID := sessionID[:min(8, len(sessionID))]
	var selectedPath string
	var selectedSession geminiSession
	var selectedTime time.Time
	ambiguous := false
	for _, entry := range entries {
		// Native exact-ID lookup considers only files with the requested short-ID suffix.
		if !strings.HasSuffix(entry.Name, "-"+shortID+".jsonl") && !strings.HasSuffix(entry.Name, "-"+shortID+".json") {
			continue
		}
		session, err := readGeminiSession(query, entry.Path)
		if err != nil {
			continue
		}
		if session.SessionID != sessionID || session.Kind == "subagent" {
			continue
		}
		updated := session.LastUpdated
		if updated.IsZero() {
			updated = session.StartTime
		}
		if selectedPath == "" || updated.After(selectedTime) {
			selectedPath, selectedSession, selectedTime = entry.Path, session, updated
			ambiguous = false
		} else if updated.Equal(selectedTime) {
			// Native ties retain readdir order, which supplies no portable file identity.
			ambiguous = true
		}
	}
	if selectedPath == "" {
		return "", fmt.Errorf("locate the Gemini session %s: %w", sessionID, sessionstore.ErrAbsent)
	}
	if ambiguous {
		return "", errors.New("the Gemini session has multiple archives with the same native update time")
	}
	if selectedSession.ProjectHash != hash {
		return "", errors.New("the selected Gemini session belongs to another project")
	}
	return selectedPath, nil
}
