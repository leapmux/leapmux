package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (h *OutputHandler) materializePlanFile(title string, content []byte, now time.Time) (string, error) {
	if h.DataDir == "" {
		return "", fmt.Errorf("missing data dir")
	}

	rootDir, err := filepath.Abs(h.DataDir)
	if err != nil {
		return "", fmt.Errorf("resolve data dir: %w", err)
	}
	dir := filepath.Join(rootDir, "plans", fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%02d", int(now.Month())))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir plan dir: %w", err)
	}

	base := agent.SanitizePlanFilenameTitle(title)
	const maxCollisions = 1000
	for counter := 0; counter <= maxCollisions; counter++ {
		filename := base
		if counter > 0 {
			filename = fmt.Sprintf("%s (%d)", base, counter)
		}
		path := filepath.Join(dir, filename+".md")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("create plan file: %w", err)
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("write plan file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close plan file: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("too many plan file collisions for %q", base)
}
