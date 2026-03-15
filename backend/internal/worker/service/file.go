package service

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// maxDirEntries is the maximum number of entries returned by ListDirectory.
// Larger directories are truncated to avoid slow transfers and unusable UIs.
const maxDirEntries = 128

// defaultReadLimit is the max bytes returned by ReadFile in a single response.
// Kept under 60KB to fit within the Noise spec 65,535-byte transport message limit
// after accounting for protobuf overhead and the 16-byte AEAD tag.
const defaultReadLimit int64 = 60 * 1024 // 60 KB

// registerFileHandlers registers handlers for file operations on the local filesystem.
func registerFileHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("ListDirectory", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ListDirectoryRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Resolve symlinks so paths are consistent (e.g. /var → /private/var on macOS).
		if resolved, err := filepath.EvalSymlinks(dirPath); err == nil {
			dirPath = resolved
		}

		entries, truncated, err := listDirectory(dirPath, dirPath, r.GetMaxDepth(), 0, r.GetDirsOnly())
		if err != nil {
			slog.Error("failed to list directory", "path", dirPath, "error", err)
			sendInternalError(sender, "failed to list directory")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ListDirectoryResponse{
			Path:      dirPath,
			Entries:   entries,
			Truncated: truncated,
		})
	})

	d.Register("ReadFile", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ReadFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		filePath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		f, err := os.Open(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				sendNotFoundError(sender, "file not found")
			} else if os.IsPermission(err) {
				sendPermissionDenied(sender, "permission denied")
			} else {
				slog.Error("failed to open file", "path", filePath, "error", err)
				sendInternalError(sender, "failed to open file")
			}
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil {
			slog.Error("failed to stat file", "path", filePath, "error", err)
			sendInternalError(sender, "failed to stat file")
			return
		}

		if info.IsDir() {
			sendInvalidArgument(sender, "path is a directory")
			return
		}

		totalSize := info.Size()

		offset := r.GetOffset()
		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				slog.Error("failed to seek file", "path", filePath, "offset", offset, "error", err)
				sendInternalError(sender, "failed to seek file")
				return
			}
		}

		limit := r.GetLimit()
		if limit <= 0 {
			limit = defaultReadLimit
		}

		buf := make([]byte, limit)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			slog.Error("failed to read file", "path", filePath, "error", err)
			sendInternalError(sender, "failed to read file")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ReadFileResponse{
			Path:      filePath,
			Content:   buf[:n],
			TotalSize: totalSize,
		})
	})

	d.Register("StatFile", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.StatFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		filePath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		info, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				sendNotFoundError(sender, "file not found")
			} else if os.IsPermission(err) {
				sendPermissionDenied(sender, "permission denied")
			} else {
				slog.Error("failed to stat file", "path", filePath, "error", err)
				sendInternalError(sender, "failed to stat file")
			}
			return
		}

		sendProtoResponse(sender, &leapmuxv1.StatFileResponse{
			Info: fileInfoToProto(info, filePath),
		})
	})
}

// sanitizePath validates and normalizes a filesystem path.
// It strips control characters, trims whitespace, rejects path traversal (..),
// expands tilde (~) using homeDir, and requires absolute paths.
func sanitizePath(path, homeDir string) (string, error) {
	// Strip control characters (< 0x20 and 0x7F).
	var b strings.Builder
	for _, r := range path {
		if r < 0x20 || r == 0x7F {
			continue
		}
		b.WriteRune(r)
	}
	s := strings.TrimSpace(b.String())

	if s == "" {
		return homeDir, nil
	}

	// Expand tilde paths.
	if s == "~" || strings.HasPrefix(s, "~/") {
		if homeDir == "" {
			return "", fmt.Errorf("cannot expand tilde: home directory not set")
		}
		if s == "~" {
			s = homeDir
		} else {
			rest := strings.TrimLeft(s[2:], "/")
			if rest == "" {
				s = homeDir
			} else {
				s = homeDir + "/" + rest
			}
		}
	}

	// Must be absolute.
	if !filepath.IsAbs(s) {
		return "", fmt.Errorf("path must be absolute: %q", s)
	}

	// Reject path traversal before normalizing (Clean resolves ".." components).
	for _, comp := range strings.Split(s, "/") {
		if comp == ".." {
			return "", fmt.Errorf("path traversal not allowed: %q", path)
		}
	}

	return filepath.Clean(s), nil
}

// fileInfoToProto converts an os.FileInfo into a protobuf FileInfo.
func fileInfoToProto(info os.FileInfo, absPath string) *leapmuxv1.FileInfo {
	return &leapmuxv1.FileInfo{
		Name:        info.Name(),
		Path:        absPath,
		IsDir:       info.IsDir(),
		Size:        info.Size(),
		ModTime:     info.ModTime().UTC().Format(time.RFC3339),
		Permissions: fmt.Sprintf("%04o", info.Mode().Perm()),
		Hidden:      isHidden(info.Name()),
	}
}

// listDirectory reads directory entries, sorts them (directories first, then
// alphabetically), truncates to maxDirEntries, and optionally merges
// single-child directories. Sorting and truncating happen on the lightweight
// DirEntry values before calling os.Stat, so we avoid stat-ing entries that
// will be discarded.
func listDirectory(dirPath, basePath string, maxDepth int32, currentDepth int32, dirsOnly bool) ([]*leapmuxv1.FileInfo, bool, error) {
	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, false, err
	}

	// When dirsOnly is set, filter out non-directory entries before sorting
	// and truncating so the entry limit counts only directories.
	if dirsOnly {
		n := 0
		for _, de := range dirEntries {
			if de.IsDir() {
				dirEntries[n] = de
				n++
			}
		}
		dirEntries = dirEntries[:n]
	}

	// Sort using DirEntry metadata (no extra syscalls).
	sort.Slice(dirEntries, func(i, j int) bool {
		iDir, jDir := dirEntries[i].IsDir(), dirEntries[j].IsDir()
		if iDir != jDir {
			return iDir
		}
		return strings.ToLower(dirEntries[i].Name()) < strings.ToLower(dirEntries[j].Name())
	})

	// Truncate before stat-ing to avoid unnecessary syscalls.
	truncated := len(dirEntries) > maxDirEntries
	if truncated {
		dirEntries = dirEntries[:maxDirEntries]
	}

	var entries []*leapmuxv1.FileInfo
	for _, de := range dirEntries {
		entryPath := filepath.Join(dirPath, de.Name())
		info, err := os.Stat(entryPath)
		if err != nil {
			// Skip entries we cannot stat (e.g. broken symlinks).
			slog.Debug("skipping unreadable entry", "path", entryPath, "error", err)
			continue
		}

		fi := fileInfoToProto(info, entryPath)

		// Merge single-child directories: if the entry is a directory, max_depth > 0,
		// and it has exactly one child that is also a directory, collapse them.
		if fi.IsDir && maxDepth > 0 && currentDepth < maxDepth {
			fi = mergeSingleChildDirs(fi, entryPath, maxDepth, currentDepth)
		}

		entries = append(entries, fi)
	}

	return entries, truncated, nil
}

// mergeSingleChildDirs recursively merges directories that contain exactly one
// child directory into a single entry (e.g. "src/main/java").
func mergeSingleChildDirs(fi *leapmuxv1.FileInfo, dirPath string, maxDepth int32, currentDepth int32) *leapmuxv1.FileInfo {
	if currentDepth >= maxDepth {
		return fi
	}

	children, err := os.ReadDir(dirPath)
	if err != nil || len(children) != 1 {
		return fi
	}

	child := children[0]
	if !child.IsDir() {
		return fi
	}

	childPath := filepath.Join(dirPath, child.Name())
	childInfo, err := os.Stat(childPath)
	if err != nil {
		return fi
	}

	merged := fileInfoToProto(childInfo, childPath)
	merged.Name = fi.Name + string(filepath.Separator) + child.Name()

	// Recursively merge if still a single child directory.
	return mergeSingleChildDirs(merged, childPath, maxDepth, currentDepth+1)
}
