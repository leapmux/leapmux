package qoder

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Qoder writes one JSONL transcript per session at
// `<config>/projects/<project-slug>/<session-uuid>.jsonl`.

const qoderProjectsDirName = "projects"

// qoderConfigDir resolves the config root from --config-dir semantics: the
// query's home under `.qoder`, or QODER_CONFIG_DIR.
func qoderConfigDir(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "QODER_CONFIG_DIR", ".qoder")
}

// qoderProjectSlug matches the installed CLI's ASCII replacement and 200-unit
// cap. The CLI hashes UTF-16 units and replaces both halves of an astral rune.
func qoderProjectSlug(path string) string {
	path = filepath.Clean(path)
	var builder strings.Builder
	builder.Grow(len(path))
	for _, ch := range path {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch > 0xFFFF:
			builder.WriteString("--")
		default:
			builder.WriteByte('-')
		}
	}
	slug := builder.String()
	if len(slug) <= 200 {
		return slug
	}
	hash := uint32(5381)
	for _, unit := range utf16.Encode([]rune(path)) {
		hash = hash*33 ^ uint32(unit)
	}
	signed := int64(int32(hash))
	if signed < 0 {
		signed = -signed
	}
	return slug[:200] + "-" + strconv.FormatInt(signed, 36)
}

// qoderTranscriptRecord is the union of the fields this reader takes. Every one
// is optional: the file holds several record types, and each fills a different
// subset.
type qoderTranscriptRecord struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	// IsMeta, IsVisibleInTranscriptOnly and IsCompactSummary mark a user record
	// that the CLI wrote, not the user: a caveat, a command output, a
	// compaction summary.
	IsMeta                    bool `json:"isMeta"`
	IsVisibleInTranscriptOnly bool `json:"isVisibleInTranscriptOnly"`
	IsCompactSummary          bool `json:"isCompactSummary"`
	// Origin states who wrote a prompt. It stays raw, so that a shape that this
	// reader did not expect cannot discard the record.
	Origin json.RawMessage `json:"origin"`
	// Title records. Qoder appends them again at the end of the file, so the
	// newest one is in the tail.
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	LastPrompt  string `json:"lastPrompt"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// qoderStoredSessions is qoderProvider.ListStoredSessions.
func qoderStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	configDir := qoderConfigDir(q)
	if configDir == "" {
		return nil, nil
	}
	projects := filepath.Join(configDir, qoderProjectsDirName)
	dir := filepath.Join(projects, qoderProjectSlug(workingDir))
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}

	limit := q.EffectiveLimit()
	candidates, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(isQoderTranscript))
	if err != nil {
		return nil, nil
	}
	sessions := sessionstore.Collect(ctx, candidates, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readQoderSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isQoderTranscript accepts the transcript files of a project directory.
func isQoderTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readQoderSession derives one session from its transcript and reports whether
// it belongs to workingDir.
func readQoderSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, atEOF, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	var (
		cwd         string
		sidechain   bool
		title       qoderTitleCandidates
		firstPrompt qoderFirstPrompt
	)
	for _, line := range head {
		var rec qoderTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		if rec.IsSidechain {
			sidechain = true
		}
		title.take(rec)
		firstPrompt.take(rec)
	}
	if !sessionstore.SameDir(cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	if sidechain {
		return agent.StoredSession{}, false
	}
	// The newest title records are at the END of the file. The tail window is
	// skipped when the head already reached the end, because it would read the
	// same records again.
	if !atEOF {
		if tail, err := sessionstore.JSONLTail(entry.Path, sessionstore.JSONLProbeBytes); err == nil {
			for _, line := range tail {
				var rec qoderTranscriptRecord
				if json.Unmarshal(line, &rec) != nil {
					continue
				}
				title.take(rec)
			}
		}
	}
	return agent.StoredSession{
		Handle:    strings.TrimSuffix(entry.Name, ".jsonl"),
		Title:     sessionstore.TrimTitle(title.best(firstPrompt.result())),
		UpdatedAt: entry.ModTime,
	}, true
}

// qoderTitleCandidates collects the title-bearing records seen so far. Each
// field keeps the LAST value seen, because the CLI appends a new record rather
// than rewriting the old one.
type qoderTitleCandidates struct {
	custom     string
	ai         string
	lastPrompt string
}

func (c *qoderTitleCandidates) take(rec qoderTranscriptRecord) {
	if rec.CustomTitle != "" {
		c.custom = rec.CustomTitle
	}
	if rec.AITitle != "" {
		c.ai = rec.AITitle
	}
	if rec.LastPrompt != "" {
		c.lastPrompt = rec.LastPrompt
	}
}

// best states the title precedence of Qoder's own session list (AGe in
// 1.1.65): the title the user set, then the title the model wrote, then the
// last prompt, then the first user message.
func (c qoderTitleCandidates) best(firstPrompt string) string {
	return sessionstore.FirstNonBlank(c.custom, c.ai, c.lastPrompt, firstPrompt)
}

// qoderFirstPrompt finds the first user message of a session the way Qoder's
// session list does (_K, Gn and Uu in 1.1.65). The first prompt that a person
// typed wins. When the head of the transcript holds none, the first command
// stands in for it.
type qoderFirstPrompt struct {
	prompt  string
	command string
}

func (p *qoderFirstPrompt) take(rec qoderTranscriptRecord) {
	if p.prompt != "" {
		return
	}
	texts, ok := qoderUserTexts(rec)
	if !ok {
		return
	}
	typed := isQoderTypedByPerson(rec.Origin)
	command := ""
	for _, text := range texts {
		kind, value := classifyQoderText(text, qoderPathExists)
		switch {
		case kind == qoderPromptText && typed:
			p.prompt = value
			return
		case kind == qoderCommandText && command == "":
			command = value
		}
	}
	if p.command == "" {
		p.command = command
	}
}

// result is the first prompt, or the first command when the head of the
// transcript holds no prompt.
func (p qoderFirstPrompt) result() string {
	return cmp.Or(p.prompt, p.command)
}

// qoderTextKind classifies one text block of a user record (cTe in 1.1.65).
type qoderTextKind int

const (
	// qoderNoText is a block that holds neither a prompt nor a command: context
	// markup, such as an attached file.
	qoderNoText qoderTextKind = iota
	// qoderPromptText is a block that holds words of the user.
	qoderPromptText
	// qoderCommandText is a block that records a command.
	qoderCommandText
)

// qoderNotices are the texts that Qoder writes as a user message for an
// interrupted request, a refused tool use, or an answer that needs no reply
// (lAe in 1.1.65). A record that opens with one holds no prompt.
var qoderNotices = map[string]bool{
	"[Request interrupted by user]":              true,
	"[Request interrupted by user for tool use]": true,
	"The user doesn't want to take this action right now. STOP what you are doing and wait for the user to tell you how to proceed.":                                                                                                                                                                                 true,
	"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). Do not repeat this call; take a different approach, or if you cannot proceed without it, tell the user what you were trying to do and why you need it.": true,
	"No response requested.": true,
}

// qoderContextTags are the elements that Qoder strips from the start of a text
// block before it reads the block (gXe and aTe in 1.1.65).
var qoderContextTags = map[string]bool{
	"system-reminder":      true,
	"hook_context":         true,
	"local-command-caveat": true,
	"loaded_context":       true,
}

var (
	qoderOpeningTag    = regexp.MustCompile(`^<([a-z][\w-]*)(?:\s[^>]*)?>`)
	qoderMarkup        = regexp.MustCompile(`^<[a-z][\w-]*(?:\s|>)`)
	qoderCommandName   = regexp.MustCompile(`<command-name>([\s\S]*?)</command-name>`)
	qoderCommandArgs   = regexp.MustCompile(`<command-args>([\s\S]*?)</command-args>`)
	qoderBashInput     = regexp.MustCompile(`<bash-input>([\s\S]*?)</bash-input>`)
	qoderCommandStdout = regexp.MustCompile(`^<local-command-stdout>([\s\S]*)</local-command-stdout>$`)
	qoderCommandWord   = regexp.MustCompile(`^[a-zA-Z0-9:_-]+$`)
)

// qoderUserTexts returns the trimmed, non-blank text blocks of a user record,
// and reports whether the record can give the first user message (sd and wi in
// 1.1.65). A record that the CLI wrote, a record that only answers a tool, a
// record that opens with a notice, and a record with no text to show give
// none.
func qoderUserTexts(rec qoderTranscriptRecord) ([]string, bool) {
	if rec.Type != "user" || rec.IsMeta || rec.IsVisibleInTranscriptOnly || rec.IsCompactSummary {
		return nil, false
	}
	blocks := sessionstore.ContentBlocks(rec.Message.Content)
	if len(blocks) > 0 && blocks[0].Type == "text" && qoderNotices[strings.TrimSpace(blocks[0].Text)] {
		return nil, false
	}
	if len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block sessionstore.ContentBlock) bool { return block.Type != "tool_result" }) {
		return nil, false
	}
	var texts []string
	shows := false
	for _, block := range blocks {
		text := strings.TrimSpace(block.Text)
		if block.Type != "text" || text == "" {
			continue
		}
		texts = append(texts, text)
		shows = shows || isQoderShownText(text)
	}
	return texts, shows
}

// isQoderShownText reports whether a text block has text that Qoder shows for
// the record (Xo in 1.1.65): a prompt, a command, or the output of a command.
func isQoderShownText(text string) bool {
	rest := stripQoderContext(text)
	switch {
	case rest == "" || qoderNotices[rest]:
		return false
	case qoderCommand(rest) != "":
		return true
	}
	if match := qoderCommandStdout.FindStringSubmatch(rest); match != nil && strings.TrimSpace(match[1]) != "" {
		return true
	}
	return !qoderMarkup.MatchString(rest)
}

// classifyQoderText classifies one trimmed text block (cTe in 1.1.65). exists
// reports whether a path exists, because Qoder reads a typed "/name" as a path,
// not a command, when "/name" exists on the disk.
func classifyQoderText(text string, exists func(string) bool) (qoderTextKind, string) {
	rest := stripQoderContext(text)
	if rest == "" {
		return qoderNoText, ""
	}
	if command := qoderCommand(rest); command != "" {
		return qoderCommandText, command
	}
	switch {
	case isQoderTypedCommand(rest, exists):
		return qoderCommandText, collapseQoderSpaces(rest)
	case qoderMarkup.MatchString(rest):
		return qoderNoText, ""
	}
	return qoderPromptText, collapseQoderSpaces(rest)
}

// stripQoderContext trims a text block and removes the context elements that
// open it (aTe in 1.1.65).
func stripQoderContext(text string) string {
	rest := strings.TrimSpace(text)
	for strings.HasPrefix(rest, "<") {
		match := qoderOpeningTag.FindStringSubmatch(rest)
		if match == nil || !qoderContextTags[match[1]] {
			break
		}
		closing := "</" + match[1] + ">"
		end := strings.Index(rest, closing)
		if end < 0 {
			break
		}
		rest = strings.TrimSpace(rest[end+len(closing):])
	}
	return rest
}

// qoderCommand returns the command that a block records, or "" (XD and cm in
// 1.1.65): a slash command with its arguments, or a shell command in the form
// "! <command>".
func qoderCommand(text string) string {
	if match := qoderCommandName.FindStringSubmatch(text); match != nil {
		if name := strings.TrimSpace(match[1]); name != "" {
			if args := qoderCommandArgs.FindStringSubmatch(text); args != nil && strings.TrimSpace(args[1]) != "" {
				return name + " " + strings.TrimSpace(args[1])
			}
			return name
		}
	}
	if match := qoderBashInput.FindStringSubmatch(text); match != nil {
		if command := strings.TrimSpace(match[1]); command != "" {
			return "! " + command
		}
	}
	return ""
}

// isQoderTypedCommand reports whether a block is a command that the user typed
// as plain text (nd and oE in 1.1.65): a help request that starts with "?", or
// a slash command whose name is a command word and not an existing root path.
func isQoderTypedCommand(text string, exists func(string) bool) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "?") {
		return true
	}
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") || strings.HasPrefix(text, "/*") {
		return false
	}
	name := text[1:]
	if end := strings.IndexFunc(name, unicode.IsSpace); end >= 0 {
		name = name[:end]
	}
	switch {
	case strings.Contains(name, "/"):
		return false
	case name == "":
		return true
	case exists("/" + name):
		return false
	}
	return qoderCommandWord.MatchString(name)
}

// collapseQoderSpaces trims text and turns each run of white space into one
// space.
func collapseQoderSpaces(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// isQoderTypedByPerson reports whether a record's origin states a prompt that a
// person typed. Qoder 1.1.65 stamps the origin "human" on such a prompt, and a
// record with no origin counts as typed.
func isQoderTypedByPerson(origin json.RawMessage) bool {
	if len(origin) == 0 || string(origin) == "null" {
		return true
	}
	var stated struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(origin, &stated) == nil && stated.Kind == "human"
}

// qoderPathExists reports whether a path exists, as the existsSync of Qoder
// does.
func qoderPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
