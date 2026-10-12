package deepseekharness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/klauspost/compress/zstd"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const maxStoredSessionBytes = 64 << 20

func harnessHome(q agent.StoredSessionQuery) string {
	if home := q.Env("DSH_HOME"); home != "" {
		return home
	}
	if home := q.Home(); home != "" {
		return filepath.Join(home, ".dsh")
	}
	return ""
}

// projectDirectory follows the native UTF-16 escaping and directory length cap.
func projectDirectory(cwd string) string {
	var text strings.Builder
	separators := false
	for _, unit := range utf16.Encode([]rune(cwd)) {
		if unit == '/' || unit == '\\' || unit == ':' {
			if !separators {
				text.WriteByte('-')
			}
			separators = true
			continue
		}
		ascii := unit != '~' && (unit >= 'A' && unit <= 'Z' || unit >= 'a' && unit <= 'z' || unit >= '0' && unit <= '9' || unit == '.' || unit == '_' || unit == '-')
		if ascii {
			text.WriteByte(byte(unit))
		} else {
			fmt.Fprintf(&text, "~%04X", unit)
		}
		separators = false
	}
	slug := strings.TrimLeft(text.String(), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 251 {
		slug = slug[:251]
	}
	return "--" + slug + "--"
}

func storedSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	home := harnessHome(q)
	if !filepath.IsAbs(home) {
		return nil, nil
	}
	rootPath := filepath.Join(home, "sessions")
	root, err := sessionstore.OpenArchiveRoot(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	projects, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, err
	}
	var candidates []struct {
		parts   []string
		updated time.Time
	}
	for _, project := range projects {
		if !project.IsDir() || q.WorkingDir != "" && project.Name() != projectDirectory(q.WorkingDir) {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(rootPath, project.Name()))
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() {
				continue
			}
			for _, leaf := range []string{"session.v4.jsonl.zstd", "session.v4.jsonl"} {
				parts := []string{project.Name(), session.Name(), leaf}
				info, err := root.Stat(filepath.Join(parts...))
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				candidates = append(candidates, struct {
					parts   []string
					updated time.Time
				}{parts, info.ModTime()})
				break
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].updated.After(candidates[j].updated) })
	limit := q.Limit
	if limit <= 0 {
		limit = agent.DefaultStoredSessionLimit
	}
	result := make([]agent.StoredSession, 0, min(limit, len(candidates)))
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, err := sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, maxStoredSessionBytes, candidate.parts...)
		if err != nil {
			continue
		}
		if strings.HasSuffix(candidate.parts[2], ".zstd") {
			decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxStoredSessionBytes))
			if err != nil {
				return nil, err
			}
			raw, err = decoder.DecodeAll(raw, nil)
			decoder.Close()
			if err != nil || len(raw) > maxStoredSessionBytes {
				continue
			}
		}
		item, ok := storedSessionFromLog(raw, q.WorkingDir, candidate.updated)
		if !ok {
			continue
		}
		result = append(result, item)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func storedSessionFromLog(raw []byte, cwd string, updated time.Time) (agent.StoredSession, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), maxStoredSessionBytes)
	if !scanner.Scan() {
		return agent.StoredSession{}, false
	}
	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		ID      string `json:"id"`
		CWD     string `json:"cwd"`
		Parent  string `json:"parentSession"`
		Origin  string `json:"origin"`
	}
	if json.Unmarshal(scanner.Bytes(), &header) != nil || header.Type != "session" || header.Version != 4 || header.ID == "" || header.Parent != "" || header.Origin == "subagent" || cwd != "" && header.CWD != cwd {
		return agent.StoredSession{}, false
	}
	item := agent.StoredSession{Handle: header.ID, UpdatedAt: updated}
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Time int64  `json:"time"`
			Data struct {
				Title string `json:"title"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "session/title" {
			item.Title = event.Data.Title
		}
		if event.Time > item.UpdatedAt.UnixMilli() {
			item.UpdatedAt = time.UnixMilli(event.Time)
		}
	}
	return item, scanner.Err() == nil
}
