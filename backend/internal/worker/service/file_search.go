package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	defaultFileSearchLimit  = 100
	maxFileSearchLimit      = 1000
	defaultFileGlobLimit    = 200
	maxFileGlobLimit        = 2000
	maxSearchableFileBytes  = 2 * 1024 * 1024 // 2 MiB
	maxSearchLineBytes      = 16 * 1024       // 16 KiB
	maxSearchLineTextLength = 500
	maxWalkedEntries        = 200_000
	fileSearchTimeout       = 30 * time.Second
)

var errSearchLimitReached = errors.New("search limit reached")

func registerFileSearchHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, dispatchPlain, svc.handleSearchFiles)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerFileServiceGlobFilesProcedure, dispatchPlain, svc.handleGlobFiles)
}

func (svc *Service) handleSearchFiles(ctx context.Context, _ channel.Caller, r *leapmuxv1.SearchFilesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(ctx, fileSearchTimeout)
	defer cancel()
	rootPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	rootPath = pathutil.Canonicalize(rootPath)

	pattern := r.GetPattern()
	if pattern == "" {
		sendInvalidArgument(sender, "pattern is required")
		return
	}
	if r.GetLimit() < 0 {
		sendInvalidArgument(sender, "limit must not be negative")
		return
	}
	limit := int(r.GetLimit())
	if limit == 0 {
		limit = defaultFileSearchLimit
	}
	if limit > maxFileSearchLimit {
		limit = maxFileSearchLimit
	}

	var matcher func(string) bool
	if r.GetRegex() {
		expr := pattern
		if r.GetIgnoreCase() {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid regular expression: %v", err))
			return
		}
		matcher = re.MatchString
	} else if r.GetIgnoreCase() {
		needle := strings.ToLower(pattern)
		matcher = func(line string) bool {
			return strings.Contains(strings.ToLower(line), needle)
		}
	} else {
		matcher = func(line string) bool {
			return strings.Contains(line, pattern)
		}
	}

	var globMatcher func(string) bool
	if glob := strings.TrimSpace(r.GetGlob()); glob != "" {
		gm, err := compileGlobPattern(glob)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid glob pattern: %v", err))
			return
		}
		globMatcher = gm
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		sendFileError(sender, "failed to stat search path", rootPath, err)
		return
	}

	var matches []*leapmuxv1.FileSearchMatch
	truncated := false

	scanFile := func(filePath, relPath string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if globMatcher != nil && !globMatcher(relPath) && !globMatcher(filepath.Base(relPath)) {
			return nil
		}
		fi, err := os.Stat(filePath)
		if err != nil || fi.IsDir() || fi.Size() > maxSearchableFileBytes || !fi.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(filePath)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()

		var probe [512]byte
		n, readErr := f.Read(probe[:])
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil
		}
		if bytes.IndexByte(probe[:n], 0) >= 0 {
			return nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil
		}

		reader := bufio.NewReaderSize(f, 8192)
		lineNo := int32(0)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lineBytes, readLineErr := readBoundedLine(reader, maxSearchLineBytes)
			if len(lineBytes) == 0 && errors.Is(readLineErr, io.EOF) {
				break
			}
			lineNo++
			line := strings.ToValidUTF8(string(lineBytes), "\uFFFD")
			if matcher(line) {
				if len(matches) >= limit {
					truncated = true
					return errSearchLimitReached
				}
				matches = append(matches, &leapmuxv1.FileSearchMatch{
					Path: relPath,
					Line: lineNo,
					Text: truncateRunes(line, maxSearchLineTextLength),
				})
			}
			if errors.Is(readLineErr, io.EOF) {
				break
			}
			if readLineErr != nil {
				return nil
			}
		}
		return nil
	}

	if !info.IsDir() {
		if err := scanFile(rootPath, filepath.Base(rootPath)); err != nil && !errors.Is(err, errSearchLimitReached) {
			if ctx.Err() != nil {
				return
			}
			sendInternalError(sender, "failed to search file")
			return
		}
	} else {
		walked := 0
		walkErr := filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			walked++
			if walked > maxWalkedEntries {
				truncated = true
				return errSearchLimitReached
			}
			if walkErr != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if p != rootPath && shouldSkipSearchDir(name) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			rel, relErr := filepath.Rel(rootPath, p)
			if relErr != nil {
				return nil
			}
			return scanFile(p, filepath.ToSlash(rel))
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			if ctx.Err() != nil {
				return
			}
			sendInternalError(sender, "failed to search directory")
			return
		}
	}

	sendProtoResponse(sender, &leapmuxv1.SearchFilesResponse{
		Matches:   matches,
		Truncated: truncated,
	})
}

func (svc *Service) handleGlobFiles(ctx context.Context, _ channel.Caller, r *leapmuxv1.GlobFilesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(ctx, fileSearchTimeout)
	defer cancel()
	rootPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	rootPath = pathutil.Canonicalize(rootPath)

	pattern := strings.TrimSpace(r.GetPattern())
	if pattern == "" {
		sendInvalidArgument(sender, "pattern is required")
		return
	}
	if r.GetLimit() < 0 {
		sendInvalidArgument(sender, "limit must not be negative")
		return
	}
	limit := int(r.GetLimit())
	if limit == 0 {
		limit = defaultFileGlobLimit
	}
	if limit > maxFileGlobLimit {
		limit = maxFileGlobLimit
	}

	matchFn, err := compileGlobPattern(pattern)
	if err != nil {
		sendInvalidArgument(sender, fmt.Sprintf("invalid glob pattern: %v", err))
		return
	}
	hasSlash := strings.Contains(filepath.ToSlash(pattern), "/")

	info, err := os.Stat(rootPath)
	if err != nil {
		sendFileError(sender, "failed to stat glob path", rootPath, err)
		return
	}
	if !info.IsDir() {
		sendInvalidArgument(sender, "path is not a directory")
		return
	}

	var paths []string
	truncated := false
	walked := 0
	walkErr := filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		walked++
		if walked > maxWalkedEntries {
			truncated = true
			return errSearchLimitReached
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if p != rootPath && shouldSkipSearchDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, p)
		if relErr != nil {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		matched := matchFn(slashRel)
		if !matched && !hasSlash {
			matched = matchFn(d.Name())
		}
		if !matched {
			return nil
		}
		if len(paths) >= limit {
			truncated = true
			return errSearchLimitReached
		}
		paths = append(paths, slashRel)
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
		if ctx.Err() != nil {
			return
		}
		sendInternalError(sender, "failed to walk directory")
		return
	}

	sendProtoResponse(sender, &leapmuxv1.GlobFilesResponse{
		Paths:     paths,
		Truncated: truncated,
	})
}

func shouldSkipSearchDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".hg", ".svn", "dist", "build", "target":
		return true
	}
	return false
}

// compileGlobPattern compiles a slash-separated glob pattern supporting `*`,
// `?`, character classes `[...]`, and `**` multi-segment wildcards.
func compileGlobPattern(pattern string) (func(string) bool, error) {
	p := filepath.ToSlash(strings.TrimSpace(pattern))
	if p == "" {
		return nil, errors.New("empty pattern")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				if i+2 < len(p) && p[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			// A leading ']' (or ']' right after '!'/'^') is a literal member of the class.
			start := i + 1
			negated := false
			if start < len(p) && (p[start] == '!' || p[start] == '^') {
				negated = true
				start++
			}
			scan := start
			if scan < len(p) && p[scan] == ']' {
				scan++
			}
			relEnd := strings.IndexByte(p[scan:], ']')
			if relEnd < 0 {
				return nil, errors.New("unclosed character class '['")
			}
			closeIdx := scan + relEnd
			cls := p[start:closeIdx]
			if cls == "" {
				return nil, errors.New("empty character class '[]'")
			}
			b.WriteByte('[')
			if negated {
				b.WriteByte('^')
				if !strings.ContainsRune(cls, '/') {
					b.WriteByte('/')
				}
			}
			b.WriteString(cls)
			b.WriteByte(']')
			i = closeIdx
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// readBoundedLine reads one newline-terminated line from r, keeping at most
// maxBytes and draining any excess bytes through the next '\n' so an oversized
// line never aborts scanning the rest of the file.
func readBoundedLine(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(frag) > 0 && len(buf) < maxBytes {
			take := min(len(frag), maxBytes-len(buf))
			buf = append(buf, frag[:take]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		buf = bytes.TrimRight(buf, "\r\n")
		return buf, err
	}
}

func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "…"
}
