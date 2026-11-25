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

	"google.golang.org/grpc/codes"

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
)

var (
	fileSearchTimeout     = 30 * time.Second
	errSearchLimitReached = errors.New("search limit reached")
)

func registerFileSearchHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, dispatchPlain, svc.handleSearchFiles)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerFileServiceGlobFilesProcedure, dispatchPlain, svc.handleGlobFiles)
}

func resolveSearchLimit(requested, defaultLimit, maxLimit int32) (int, error) {
	if requested < 0 {
		return 0, errors.New("limit must not be negative")
	}
	limit := int(requested)
	if limit == 0 {
		limit = int(defaultLimit)
	}
	if limit > int(maxLimit) {
		limit = int(maxLimit)
	}
	return limit, nil
}

// sendSearchContextError reports a local handler timeout to the caller when the
// parent RPC context is still open. It returns true whenever searchCtx has ended.
func sendSearchContextError(parentCtx, searchCtx context.Context, sender channel.ResponseWriter, timeoutMsg string) bool {
	if searchCtx.Err() == nil {
		return false
	}
	if parentCtx.Err() != nil {
		return true
	}
	_ = sender.SendError(int32(codes.DeadlineExceeded), timeoutMsg)
	return true
}

func walkSearchDir(ctx context.Context, rootPath string, skipSymlinks bool, truncated *bool, visit func(p, slashRel string, d fs.DirEntry) error) error {
	walked := 0
	return filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		walked++
		if walked > maxWalkedEntries {
			*truncated = true
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
		if skipSymlinks && d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, p)
		if relErr != nil {
			return nil
		}
		return visit(p, filepath.ToSlash(rel), d)
	})
}

func (svc *Service) handleSearchFiles(parentCtx context.Context, _ channel.Caller, r *leapmuxv1.SearchFilesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(parentCtx, fileSearchTimeout)
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
	limit, err := resolveSearchLimit(r.GetLimit(), defaultFileSearchLimit, maxFileSearchLimit)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}

	var matcher func([]byte) bool
	if r.GetRegex() || r.GetIgnoreCase() {
		expr := pattern
		if !r.GetRegex() {
			expr = regexp.QuoteMeta(expr)
		}
		if r.GetIgnoreCase() {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid regular expression: %v", err))
			return
		}
		matcher = re.Match
	} else {
		needle := []byte(pattern)
		matcher = func(line []byte) bool {
			return bytes.Contains(line, needle)
		}
	}

	var globMatcher func(string) bool
	globHasSlash := false
	if glob := strings.TrimSpace(r.GetGlob()); glob != "" {
		gm, err := compileGlobPattern(glob)
		if err != nil {
			sendInvalidArgument(sender, fmt.Sprintf("invalid glob pattern: %v", err))
			return
		}
		globMatcher = gm
		globHasSlash = strings.Contains(filepath.ToSlash(glob), "/")
	}

	info, err := os.Stat(rootPath)
	if err != nil {
		sendFileError(sender, "failed to stat search path", rootPath, err)
		return
	}

	var matches []*leapmuxv1.FileSearchMatch
	truncated := false
	reader := bufio.NewReaderSize(nil, 8192)
	lineBuf := make([]byte, 0, 256)

	scanFile := func(filePath, relPath, baseName string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if globMatcher != nil && !globMatcher(relPath) && (globHasSlash || !globMatcher(baseName)) {
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

		reader.Reset(f)
		probe, peekErr := reader.Peek(512)
		if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
			return nil
		}
		if bytes.IndexByte(probe, 0) >= 0 {
			return nil
		}

		safeRelPath := ""
		lineNo := int32(0)
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var lineBytes []byte
			var readLineErr error
			lineBytes, lineBuf, readLineErr = readLimitedLine(reader, maxSearchLineBytes, lineBuf)
			if len(lineBytes) == 0 && errors.Is(readLineErr, io.EOF) {
				break
			}
			lineNo++
			if matcher(lineBytes) {
				if len(matches) >= limit {
					truncated = true
					return errSearchLimitReached
				}
				if safeRelPath == "" {
					safeRelPath = strings.ToValidUTF8(relPath, "\uFFFD")
				}
				line := strings.ToValidUTF8(string(lineBytes), "\uFFFD")
				matches = append(matches, &leapmuxv1.FileSearchMatch{
					Path: safeRelPath,
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
		base := filepath.Base(rootPath)
		if err := scanFile(rootPath, base, base); err != nil && !errors.Is(err, errSearchLimitReached) {
			if sendSearchContextError(parentCtx, ctx, sender, "file search timed out") {
				return
			}
			sendInternalError(sender, "failed to search file")
			return
		}
	} else {
		walkErr := walkSearchDir(ctx, rootPath, true, &truncated, func(p, slashRel string, d fs.DirEntry) error {
			return scanFile(p, slashRel, d.Name())
		})
		if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
			if sendSearchContextError(parentCtx, ctx, sender, "file search timed out") {
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

func (svc *Service) handleGlobFiles(parentCtx context.Context, _ channel.Caller, r *leapmuxv1.GlobFilesRequest, sender channel.ResponseWriter) {
	ctx, cancel := context.WithTimeout(parentCtx, fileSearchTimeout)
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
	limit, err := resolveSearchLimit(r.GetLimit(), defaultFileGlobLimit, maxFileGlobLimit)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
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
	walkErr := walkSearchDir(ctx, rootPath, false, &truncated, func(_ string, slashRel string, d fs.DirEntry) error {
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
		paths = append(paths, strings.ToValidUTF8(slashRel, "\uFFFD"))
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchLimitReached) {
		if sendSearchContextError(parentCtx, ctx, sender, "glob search timed out") {
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

type globTokenKind uint8

const (
	globTokLit globTokenKind = iota
	globTokAnyOne
	globTokAnyMany
	globTokClass
)

type globRange struct {
	lo, hi rune
}

type globToken struct {
	kind    globTokenKind
	lit     rune
	negated bool
	ranges  []globRange
}

func (tok globToken) matchesRune(r rune) bool {
	switch tok.kind {
	case globTokLit:
		return r == tok.lit
	case globTokAnyOne:
		return true
	case globTokClass:
		inClass := false
		for _, rg := range tok.ranges {
			if r >= rg.lo && r <= rg.hi {
				inClass = true
				break
			}
		}
		if tok.negated {
			return !inClass
		}
		return inClass
	default:
		return false
	}
}

type globSegment struct {
	doublestar bool
	tokens     []globToken
}

func compileGlobSegment(seg string) (globSegment, error) {
	if seg == "**" {
		return globSegment{doublestar: true}, nil
	}
	runes := []rune(seg)
	tokens := make([]globToken, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			// Collapse adjacent '*' within a single segment.
			if len(tokens) == 0 || tokens[len(tokens)-1].kind != globTokAnyMany {
				tokens = append(tokens, globToken{kind: globTokAnyMany})
			}
		case '?':
			tokens = append(tokens, globToken{kind: globTokAnyOne})
		case '[':
			start := i + 1
			negated := false
			if start < len(runes) && (runes[start] == '!' || runes[start] == '^') {
				negated = true
				start++
			}
			scan := start
			// A leading ']' right after '[' or '[!' / '[^' is a literal member.
			if scan < len(runes) && runes[scan] == ']' {
				scan++
			}
			closeIdx := -1
			for j := scan; j < len(runes); j++ {
				if runes[j] == ']' {
					closeIdx = j
					break
				}
			}
			if closeIdx < 0 {
				return globSegment{}, errors.New("unclosed character class '['")
			}
			cls := runes[start:closeIdx]
			if len(cls) == 0 {
				return globSegment{}, errors.New("empty character class '[]'")
			}
			ranges := make([]globRange, 0, len(cls))
			for j := 0; j < len(cls); j++ {
				if j+2 < len(cls) && cls[j+1] == '-' {
					lo, hi := cls[j], cls[j+2]
					if lo > hi {
						return globSegment{}, fmt.Errorf("invalid character range '%c-%c'", lo, hi)
					}
					ranges = append(ranges, globRange{lo: lo, hi: hi})
					j += 2
				} else {
					ranges = append(ranges, globRange{lo: cls[j], hi: cls[j]})
				}
			}
			tokens = append(tokens, globToken{kind: globTokClass, negated: negated, ranges: ranges})
			i = closeIdx
		default:
			tokens = append(tokens, globToken{kind: globTokLit, lit: runes[i]})
		}
	}
	return globSegment{tokens: tokens}, nil
}

func matchGlobSegment(tokens []globToken, seg string) bool {
	runes := []rune(seg)
	ti, ri := 0, 0
	starIdx, matchIdx := -1, 0
	for ri < len(runes) {
		if ti < len(tokens) && tokens[ti].kind == globTokAnyMany {
			starIdx = ti
			matchIdx = ri
			ti++
		} else if ti < len(tokens) && tokens[ti].matchesRune(runes[ri]) {
			ti++
			ri++
		} else if starIdx >= 0 {
			ti = starIdx + 1
			matchIdx++
			ri = matchIdx
		} else {
			return false
		}
	}
	for ti < len(tokens) && tokens[ti].kind == globTokAnyMany {
		ti++
	}
	return ti == len(tokens)
}

// compileGlobPattern compiles a slash-separated glob pattern supporting `*`,
// `?`, character classes `[...]`, and `**` multi-segment wildcards.
func compileGlobPattern(pattern string) (func(string) bool, error) {
	p := filepath.ToSlash(strings.TrimSpace(pattern))
	if p == "" {
		return nil, errors.New("empty pattern")
	}
	rawSegs := strings.Split(p, "/")
	segs := make([]globSegment, len(rawSegs))
	for i, raw := range rawSegs {
		compiled, err := compileGlobSegment(raw)
		if err != nil {
			return nil, err
		}
		segs[i] = compiled
	}
	trailingDoublestar := len(segs) > 1 && segs[len(segs)-1].doublestar

	return func(candidate string) bool {
		pathSegs := strings.Split(candidate, "/")
		pi, si := 0, 0
		starPi, starSi := -1, 0
		for si < len(pathSegs) {
			if pi < len(segs) && segs[pi].doublestar {
				if pi == len(segs)-1 && trailingDoublestar {
					return true
				}
				starPi = pi
				starSi = si
				pi++
			} else if pi < len(segs) && matchGlobSegment(segs[pi].tokens, pathSegs[si]) {
				pi++
				si++
			} else if starPi >= 0 {
				pi = starPi + 1
				starSi++
				si = starSi
			} else {
				return false
			}
		}
		for pi < len(segs) && segs[pi].doublestar {
			if pi == len(segs)-1 && trailingDoublestar {
				return false
			}
			pi++
		}
		return pi == len(segs)
	}, nil
}

// readLimitedLine reads one newline-terminated line from r, keeping at most
// maxBytes and draining any excess bytes through the next '\n' so an oversized
// line never aborts scanning the rest of the file. When the full line fits in
// r's internal buffer, the returned slice aliases r's buffer without copying.
func readLimitedLine(r *bufio.Reader, maxBytes int, scratch []byte) ([]byte, []byte, error) {
	frag, err := r.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		if len(frag) > maxBytes {
			frag = frag[:maxBytes]
		}
		return bytes.TrimRight(frag, "\r\n"), scratch, err
	}
	buf := scratch[:0]
	if len(frag) > 0 {
		take := min(len(frag), maxBytes)
		buf = append(buf, frag[:take]...)
	}
	for {
		frag, err = r.ReadSlice('\n')
		if len(frag) > 0 && len(buf) < maxBytes {
			take := min(len(frag), maxBytes-len(buf))
			buf = append(buf, frag[:take]...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), buf, err
	}
}

func truncateRunes(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "…"
}
