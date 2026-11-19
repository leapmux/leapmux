package dirac

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const (
	diracTranscriptLimit      = 16 << 20
	diracTaskHistoryLimit     = 4 << 20
	diracArchiveRetryInterval = 250 * time.Millisecond
	diracArchiveRetryMax      = 4 * time.Second
	diracArchiveTimerTag      = "dirac-child-store"
)

func safeDiracComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\`)
}

type diracArchiveFileOpener interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (*os.File, error)
}

func readDiracRegularFile(opener diracArchiveFileOpener, path string, limit int64) ([]byte, error) {
	data, err := sessionstore.ReadRegularFile(opener, path, limit)
	if err != nil {
		return nil, fmt.Errorf("read the dirac archive file: %w", err)
	}
	return data, nil
}
