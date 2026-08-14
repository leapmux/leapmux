package service

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/validate"
)

// maxDirEntries is the maximum number of entries returned by ListDirectory.
// Larger directories are truncated to avoid slow transfers and unusable UIs.
const maxDirEntries = 256

// defaultReadLimit is the max bytes returned by ReadFile when the request
// omits limit (or passes <= 0). Kept near one Noise transport chunk so a
// default page stays a single chunk after protobuf/AEAD overhead; callers
// that need more raise limit explicitly up to maxReadLimit.
const defaultReadLimit int64 = 60 * 1024 // 60 KB

// maxReadLimit caps what a ReadFile request may ask for, whatever it
// asks for.
//
// It is the same producer ceiling the agent stdout scanners bound
// themselves by, for the same reason: a response above it is one the
// receiver refuses, and on the unary path that refusal surfaces as
// ResourceExhausted. Without the clamp the limit field also picks the
// worker's allocation size, so a single request could ask it to reserve
// gigabytes. Uses the tighter of the worker's configured max_message_size
// and the channel's negotiated payload budget (min(hub, worker)) when the
// writer is channel-backed.
func (svc *Service) maxReadLimit(sender channel.ResponseWriter) int64 {
	configured := channelwire.ResolveMaxMessageSize(svc.MaxMessageSize)
	if sender != nil {
		if n := sender.MaxPayloadBudget(); n > 0 && n < configured {
			return int64(n)
		}
	}
	return int64(configured)
}

// registerFileHandlers registers handlers for file operations on the local filesystem.
func registerFileHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register("ListDirectory", func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ListDirectoryRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Resolve symlinks so paths are consistent (e.g. /var → /private/var on macOS).
		dirPath = pathutil.Canonicalize(dirPath)

		entries, truncated, totalEntries, err := listDirectory(dirPath, dirPath, r.GetMaxDepth(), 0, r.GetDirsOnly())
		if err != nil {
			slog.Error("failed to list directory", "path", dirPath, "error", err)
			sendInternalError(sender, "failed to list directory")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ListDirectoryResponse{
			Path:         dirPath,
			Entries:      entries,
			Truncated:    truncated,
			TotalEntries: int32(totalEntries),
		})
	})

	d.Register("ReadFile", func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ReadFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		filePath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
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
		limit := r.GetLimit()
		if limit <= 0 {
			limit = defaultReadLimit
		}
		// Clamp against the producer ceiling. limit comes straight off the
		// request and is used below as make([]byte, limit), so an
		// unclamped value is a request field that chooses the worker's
		// allocation size -- and any value over the ceiling also builds a
		// response the channel then refuses, which sendProtoResponse
		// cannot report. Truncating is the honest answer: the response
		// already carries total_size, so a caller reading a large file
		// pages rather than being told nothing.
		if max := svc.maxReadLimit(sender); limit > max {
			limit = max
		}

		// meta_only_if_truncated: when the file would be truncated by the
		// read window, return total_size with an empty content payload so
		// callers can detect oversize files (e.g. images we won't preview)
		// without paying for the bytes. Lets the file viewer collapse a
		// StatFile + ReadFile pair into one round trip.
		if r.GetMetaOnlyIfTruncated() && totalSize > offset+limit {
			sendProtoResponse(sender, &leapmuxv1.ReadFileResponse{
				Path:      filePath,
				Content:   nil,
				TotalSize: totalSize,
				ModTime:   formatModTime(info.ModTime()),
			})
			return
		}

		if offset > 0 {
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				slog.Error("failed to seek file", "path", filePath, "offset", offset, "error", err)
				sendInternalError(sender, "failed to seek file")
				return
			}
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
			ModTime:   formatModTime(info.ModTime()),
		})
	})

	d.Register("StatFile", func(ctx context.Context, userID userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.StatFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		filePath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
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

// modTimeLayout is the layout every modification time on the wire uses.
//
// RFC3339 with a FIXED-WIDTH nanosecond field. Two properties matter, and both
// break if someone substitutes a stock layout:
//
//   - time.RFC3339 truncates to whole seconds. A formatter or a codemod writes
//     many files inside one second, and the sidebar's "Newest first" order then
//     ties them all and falls back to the name -- which is the alphabetical
//     order the user chose a time order to escape.
//   - time.RFC3339Nano TRIMS trailing zeros, so the width varies. The frontend
//     comparator compares these strings LEXICOGRAPHICALLY instead of parsing
//     them, which is only chronological while the width is constant. The
//     literal ".000000000" forces all nine digits; UTC renders the offset as a
//     one-character "Z", so every value is exactly 30 characters.
//
// time.Parse(time.RFC3339, …) accepts the fractional part, so this stays
// readable by any RFC3339 consumer.
const modTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatModTime renders a modification time in modTimeLayout, in UTC.
func formatModTime(t time.Time) string {
	return t.UTC().Format(modTimeLayout)
}

// fileInfoToProto converts an os.FileInfo into a protobuf FileInfo.
func fileInfoToProto(info os.FileInfo, absPath string) *leapmuxv1.FileInfo {
	return &leapmuxv1.FileInfo{
		Name:        info.Name(),
		Path:        absPath,
		IsDir:       info.IsDir(),
		Size:        info.Size(),
		ModTime:     formatModTime(info.ModTime()),
		Permissions: fmt.Sprintf("%04o", info.Mode().Perm()),
		Hidden:      isHidden(absPath, info.Name()),
	}
}

// isDirEntry reports whether de is a directory or a symlink to a directory
// under parentDir. It follows symlinks, unlike de.IsDir().
func isDirEntry(de os.DirEntry, parentDir string) bool {
	if de.IsDir() {
		return true
	}
	if de.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parentDir, de.Name()))
	return err == nil && info.IsDir()
}

// sortableDirEntry carries the two values the listing order needs, computed
// once per entry. Both are expensive to derive in a comparator: isDirEntry
// runs os.Stat for a symlink, and strings.ToLower allocates.
type sortableDirEntry struct {
	de        os.DirEntry
	isDir     bool
	lowerName string
}

// listDirectory reads directory entries, sorts them (directories first, then
// alphabetically), truncates to maxDirEntries, and optionally merges
// single-child directories. The third return value is how many entries the
// directory held BEFORE truncation, so the caller can report the size of what
// it is not showing.
//
// Symlink resolution happens exactly once per entry, before the sort, and the
// os.Stat that builds each returned FileInfo happens only after truncation, so
// we never stat an entry that will be discarded.
func listDirectory(dirPath, basePath string, maxDepth int32, currentDepth int32, dirsOnly bool) ([]*leapmuxv1.FileInfo, bool, int, error) {
	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, false, 0, err
	}

	// One pass computes the sort keys and applies the dirs-only filter, so
	// isDirEntry runs N times rather than once per comparison. It resolves a
	// symlink with os.Stat, and a comparator would repeat that O(N log N)
	// times for a directory of symlinks.
	//
	// The dirs-only filter MUST stay here, before the sort and the truncation
	// below, so that maxDirEntries counts only directories. Move it after the
	// truncation and a directory that holds 256 files plus one subdirectory
	// returns no subdirectory at all to the picker.
	decorated := make([]sortableDirEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		isDir := isDirEntry(de, dirPath)
		if dirsOnly && !isDir {
			continue
		}
		decorated = append(decorated, sortableDirEntry{
			de:        de,
			isDir:     isDir,
			lowerName: strings.ToLower(de.Name()),
		})
	}

	slices.SortFunc(decorated, func(a, b sortableDirEntry) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.lowerName, b.lowerName)
	})

	// Counted before the slice below, and after the dirs-only filter, so it
	// describes the same population the entries come from.
	totalEntries := len(decorated)
	// Truncate before stat-ing to avoid unnecessary syscalls.
	truncated := totalEntries > maxDirEntries
	if truncated {
		decorated = decorated[:maxDirEntries]
	}

	var entries []*leapmuxv1.FileInfo
	for _, sde := range decorated {
		de := sde.de
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
		// The hidden flag is propagated so the frontend can still filter merged entries.
		if fi.IsDir && maxDepth > 0 && currentDepth < maxDepth {
			fi = mergeSingleChildDirs(fi, entryPath, maxDepth, currentDepth)
		}

		entries = append(entries, fi)
	}

	return entries, truncated, totalEntries, nil
}

// mergeReadTimeout bounds how long a single readDirN call may block.
// macOS TCC-protected directories (e.g. ~/Library/Messages) can hang
// os.Open or ReadDir indefinitely; this prevents the ListDirectory
// handler from stalling.
const mergeReadTimeout = 500 * time.Millisecond

// readDirN reads at most n entries from a directory within mergeReadTimeout.
// Returns an error if the directory cannot be opened or read in time.
func readDirN(dirPath string, n int) ([]os.DirEntry, error) {
	return readDirNWithTimeout(dirPath, n, mergeReadTimeout)
}

// readDirNWithTimeout is readDirN with a caller-supplied timeout, exposed
// for tests.
func readDirNWithTimeout(dirPath string, n int, timeout time.Duration) ([]os.DirEntry, error) {
	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)

	// fd is populated once os.Open returns successfully. On timeout we close
	// it to release the descriptor and, on platforms where a concurrent
	// close unblocks a pending read, to let the goroutine exit promptly.
	// If os.Open is still in flight when the timeout fires, the goroutine
	// keeps running until the syscall returns — Go has no portable way to
	// cancel an in-flight syscall — but it will close the fd itself before
	// exiting, so nothing is permanently leaked.
	var fd atomic.Pointer[os.File]

	go func() {
		f, err := os.Open(dirPath)
		if err != nil {
			ch <- result{nil, err}
			return
		}
		fd.Store(f)
		entries, err := f.ReadDir(n)
		_ = f.Close()
		ch <- result{entries, err}
	}()

	select {
	case r := <-ch:
		return r.entries, r.err
	case <-time.After(timeout):
		if f := fd.Load(); f != nil {
			_ = f.Close()
		}
		return nil, fmt.Errorf("readDirN timed out for %s", dirPath)
	}
}

// mergeSingleChildDirs recursively merges directories that contain exactly one
// child directory into a single entry (e.g. "src/main/java").
func mergeSingleChildDirs(fi *leapmuxv1.FileInfo, dirPath string, maxDepth int32, currentDepth int32) *leapmuxv1.FileInfo {
	if currentDepth >= maxDepth {
		return fi
	}

	children, err := readDirN(dirPath, 2)
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
	// Breadcrumb label; always '/' so rendering is uniform across OSes.
	merged.Name = fi.Name + "/" + child.Name()
	if fi.Hidden {
		merged.Hidden = true
	}

	// Recursively merge if still a single child directory.
	return mergeSingleChildDirs(merged, childPath, maxDepth, currentDepth+1)
}
