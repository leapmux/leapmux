package letta

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
)

// Letta writes the complete final report to a task log. The task notification
// can clip that report, so the child tab reads the log through a confined root.
const lettaTaskLogReadLimit = contracts.MaxMessageSize - 4096

// lettaTaskLogRoot follows the environment that the native process receives.
// LETTA_SCRATCHPAD replaces Letta's generated directory under the temp root.
func lettaTaskLogRoot(env []string) (string, bool) {
	for index := len(env) - 1; index >= 0; index-- {
		if value, ok := strings.CutPrefix(env[index], "LETTA_SCRATCHPAD="); ok && value != "" {
			if filepath.IsAbs(value) {
				return filepath.Clean(value), true
			}
			return os.TempDir(), false
		}
	}
	return os.TempDir(), false
}

// readLettaTaskReport accepts only the native file for the linked task and
// child. It does not follow a symlink, and it rejects an oversized log.
func readLettaTaskReport(rootPath string, direct bool, outputFile, taskID, childID, status string) (report string, err error) {
	if !filepath.IsAbs(rootPath) || !filepath.IsAbs(outputFile) || !safeLettaTaskID(taskID) || childID == "" {
		return "", errors.New("letta child task log identity is incomplete")
	}
	rootPath = filepath.Clean(rootPath)
	rel, err := filepath.Rel(rootPath, outputFile)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("letta child task log leaves its root")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if direct {
		if len(parts) != 1 || parts[0] != taskID+".log" {
			return "", errors.New("letta child task log is not the task file")
		}
	} else if len(parts) != 2 || !strings.HasPrefix(parts[0], "letta-background-") ||
		parts[0] == "letta-background-" || parts[1] != taskID+".log" {
		return "", errors.New("letta child task log is not in a native background directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if !direct {
		info, statErr := root.Lstat(parts[0])
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("letta child task directory is not a regular directory")
		}
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > lettaTaskLogReadLimit {
		return "", errors.New("letta child task log is not a regular file within the size cap")
	}
	file, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return "", errors.New("letta child task log changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(file, lettaTaskLogReadLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > lettaTaskLogReadLimit {
		return "", errors.New("letta child task log exceeds the size cap")
	}
	return parseLettaTaskLog(string(data), childID, status)
}

func parseLettaTaskLog(content, childID, status string) (string, error) {
	start := strings.LastIndex(content, "\nsubagent_type=")
	if start >= 0 {
		start++
	} else if strings.HasPrefix(content, "subagent_type=") {
		start = 0
	} else {
		return "", errors.New("letta child task log has no result header")
	}
	headerEnd := strings.IndexByte(content[start:], '\n')
	if headerEnd < 0 {
		return "", errors.New("letta child task log has no report")
	}
	header := content[start : start+headerEnd]
	if !lettaReportIdentifiesChild(header, childID, status) {
		return "", errors.New("letta child task log identifies another child or status")
	}
	rest := content[start+headerEnd+1:]
	if !strings.HasPrefix(rest, "\n") {
		return "", errors.New("letta child task log has no report separator")
	}
	rest = rest[1:]
	suffix := "\n\n[Task completed]\n"
	if status == "failed" {
		suffix = "\n\n[Task failed]\n"
	}
	if !strings.HasSuffix(rest, suffix) {
		return "", fmt.Errorf("letta child task log has no %s ending", status)
	}
	return strings.TrimSuffix(rest, suffix), nil
}
