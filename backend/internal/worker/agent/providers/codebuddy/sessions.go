package codebuddy

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// errControlTimeout reports a control request that got no answer in time.
var errControlTimeout = errors.New("timeout waiting for agent to respond")

// errString wraps a control error message as an error.
func errString(msg string) error {
	if msg == "" {
		return errors.New("control request failed")
	}
	return errors.New(msg)
}

// shortID generates a short correlation id for a control request.
func shortID() string { return id.Short() }

// CodeBuddy writes one JSONL transcript per session at
// `<config root>/projects/<mangled cwd>/<session-id>.jsonl`. The mangling
// replaces every `/` with `-`. There is no index, so this reader finds the
// project directory the same way and reads the newest transcripts.

const codebuddyProjectsDirName = "projects"

// codebuddyConfigDir resolves `$CODEBUDDY_CONFIG_DIR`, default `~/.codebuddy`.
func codebuddyConfigDir(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "CODEBUDDY_CONFIG_DIR", ".codebuddy")
}

// codebuddyProjectSlug follows the CLI's compressPath rule for its project
// directory. The CLI resolves existing paths first, folds three separators,
// and shortens a slug over 255 UTF-8 bytes with a byte-wise DJB2 suffix.
func codebuddyProjectSlug(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	var builder strings.Builder
	previousHyphen := false
	for _, ch := range path {
		if ch == '/' || ch == '\\' || ch == ':' || ch == '-' {
			if builder.Len() > 0 && !previousHyphen {
				builder.WriteByte('-')
				previousHyphen = true
			}
			continue
		}
		builder.WriteRune(ch)
		previousHyphen = false
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) <= 255 {
		return slug
	}
	var hash uint32 = 5381
	for _, value := range []byte(slug) {
		hash = hash*33 ^ uint32(value)
	}
	var prefix strings.Builder
	for _, ch := range slug {
		if prefix.Len()+len(string(ch)) > 180 {
			break
		}
		prefix.WriteRune(ch)
	}
	return prefix.String() + "-" + strconv.FormatUint(uint64(hash), 36)
}

// codebuddyTranscriptRecord is the union of the fields this reader takes from a
// transcript line. Every one is optional: the file holds several record types,
// and each fills a different subset.
//
// CodeBuddy 2.160.0 writes a user message as a "message" record. Its role and
// its content sit at the top level of the record, beside the session id and the
// cwd, and a text block of the content has the type input_text:
//
//	{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}],
//	 "providerData":{"agent":"cli"},"sessionId":"…","cwd":"…"}
type codebuddyTranscriptRecord struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Cwd       string          `json:"cwd"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	// ProviderData stays raw. Each record type gives it a different shape, and
	// a shape that this reader did not expect must not discard the record.
	ProviderData json.RawMessage `json:"providerData"`
	// Title records the CLI writes.
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Topic       string `json:"topic"`
}

// codebuddyStoredSessions is codebuddyProvider.ListStoredSessions.
func codebuddyStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	configDir := codebuddyConfigDir(q)
	if configDir == "" {
		return nil, nil
	}
	projects := filepath.Join(configDir, codebuddyProjectsDirName)
	dir := filepath.Join(projects, codebuddyProjectSlug(workingDir))
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}

	limit := q.EffectiveLimit()
	candidates, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(isCodebuddyTranscript))
	if err != nil {
		return nil, nil
	}
	sessions := sessionstore.Collect(ctx, candidates, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readCodebuddySession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isCodebuddyTranscript accepts the transcript files of a project directory.
func isCodebuddyTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readCodebuddySession derives one session from its transcript and reports
// whether it belongs to workingDir.
func readCodebuddySession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, atEOF, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	var cwd, firstPrompt string
	var title codebuddyTitleCandidates
	for _, line := range head {
		var rec codebuddyTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		title.take(rec)
		if firstPrompt == "" {
			firstPrompt = codebuddyRealUserPrompt(rec)
		}
	}
	if !sessionstore.SameDir(cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	if !atEOF {
		if tail, err := sessionstore.JSONLTail(entry.Path, sessionstore.JSONLProbeBytes); err == nil {
			for _, line := range tail {
				var rec codebuddyTranscriptRecord
				if json.Unmarshal(line, &rec) != nil {
					continue
				}
				title.take(rec)
			}
		}
	}
	return agent.StoredSession{
		Handle:    strings.TrimSuffix(entry.Name, ".jsonl"),
		Title:     sessionstore.TrimTitle(title.best(firstPrompt)),
		UpdatedAt: entry.ModTime,
	}, true
}

// codebuddyTitleCandidates collects the title-bearing records seen so far. Each
// field keeps the LAST value seen, because the CLI appends a new record rather
// than rewriting the old one.
type codebuddyTitleCandidates struct {
	custom string
	ai     string
	topic  string
}

func (c *codebuddyTitleCandidates) take(rec codebuddyTranscriptRecord) {
	if strings.TrimSpace(rec.CustomTitle) != "" {
		c.custom = rec.CustomTitle
	}
	if !isCodebuddyPlaceholderTitle(rec.AITitle) {
		c.ai = rec.AITitle
	}
	if !isCodebuddyPlaceholderTitle(rec.Topic) {
		c.topic = rec.Topic
	}
}

// best states the title precedence of CodeBuddy's own session list
// (getSessionName in 2.160.0): the title the user set, then the title the model
// wrote, then the topic, then the first prompt.
func (c codebuddyTitleCandidates) best(firstPrompt string) string {
	return sessionstore.FirstNonBlank(c.custom, c.ai, c.topic, firstPrompt)
}

// isCodebuddyPlaceholderTitle reports whether a generated title holds no title,
// so that the session list of CodeBuddy skips it (Ym in 2.160.0): a blank
// title, the "(No content)" placeholder, the /compact command, or the path
// block of an image.
func isCodebuddyPlaceholderTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || title == "(No content)" || title == "/compact" || isCodebuddyImagePathBlock(title)
}

// codebuddyRealUserPrompt returns the prompt of a real user message, or "" for
// any other record. CodeBuddy saves the same words as the initial summary of a
// session (ensureInitialSummary, isRealUserMessage and extractTextContent in
// 2.160.0).
//
// A real user message is a user message record that starts a model run and
// holds a prompt. A local command writes its records with skipRun, so a
// command such as /model never gives the title.
func codebuddyRealUserPrompt(rec codebuddyTranscriptRecord) string {
	if rec.Type != "message" || rec.Role != "user" || codebuddySkipsRun(rec.ProviderData) {
		return ""
	}
	return codebuddyPromptText(rec.Content)
}

// codebuddySkipsRun reports whether the providerData of a record marks a record
// that starts no model run.
func codebuddySkipsRun(providerData json.RawMessage) bool {
	var data struct {
		SkipRun bool `json:"skipRun"`
	}
	return len(providerData) > 0 && json.Unmarshal(providerData, &data) == nil && data.SkipRun
}

// codebuddyPromptText reads the words of the user from the content of a user
// message, the way CodeBuddy's extractTextContent does. The LAST plain
// input_text block wins, so a prompt after its attachments is the prompt. The
// reader skips these blocks:
//   - A block that opens with a system reminder, such as the local-command
//     caveat.
//   - A /clear command.
//   - The path block that CodeBuddy adds after an image.
//
// A block that carries a command, a shell input or a memory input gives that
// input, and so does a block whose providerData carries its visible content.
// Each of these ends the search. The reader removes the system reminders and
// the user_query tags from a plain block. It leaves a team message as its raw
// text, where CodeBuddy shortens it to the name of the teammate and a summary.
func codebuddyPromptText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return codebuddyStripPromptContext(text)
	}
	var items []json.RawMessage
	if json.Unmarshal(content, &items) != nil {
		return ""
	}
	prompt := ""
	for _, item := range items {
		var block struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			ProviderData struct {
				Content *string `json:"content"`
			} `json:"providerData"`
		}
		if json.Unmarshal(item, &block) != nil || block.Type != "input_text" ||
			isCodebuddySystemInternalBlock(block.Text) || isCodebuddyImagePathBlock(block.Text) {
			continue
		}
		if input, ok := codebuddyStructuredInput(block.Text); ok {
			prompt = input
			break
		}
		if block.ProviderData.Content != nil {
			prompt = *block.ProviderData.Content
			break
		}
		prompt = codebuddyStripPromptContext(block.Text)
	}
	return strings.TrimSpace(prompt)
}

var (
	codebuddyClearCommand   = regexp.MustCompile(`<command_name>\s*/clear\s*</command_name>`)
	codebuddyImagePathBlock = regexp.MustCompile(`^<image_local_path>[\s\S]*</image_local_path>$`)
	codebuddySystemReminder = regexp.MustCompile(`<system-reminder\b[^>]*>[\s\S]*?</system-reminder>\s*`)
	codebuddyUserQueryTags  = regexp.MustCompile(`<user_query>([\s\S]*?)</user_query>`)
)

// isCodebuddySystemInternalBlock reports whether a text block holds a message
// of the CLI rather than of the user (isSystemInternalMessage in 2.160.0): a
// block that opens with a system reminder and holds no user_query, which
// includes the local-command caveat, or a /clear command.
func isCodebuddySystemInternalBlock(text string) bool {
	trimmed := strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "<system-reminder") && !strings.Contains(trimmed, "<user_query>") ||
		codebuddyClearCommand.MatchString(text)
}

// isCodebuddyImagePathBlock reports whether a text block is the path block that
// CodeBuddy adds after an image (isImageLocalPathBlock in 2.160.0).
func isCodebuddyImagePathBlock(text string) bool {
	return codebuddyImagePathBlock.MatchString(strings.TrimSpace(text))
}

// codebuddyStripPromptContext removes the system reminders and the user_query
// tags from a block, and trims it (stripPromptContextXml in 2.160.0). The text
// inside the user_query tags stays.
func codebuddyStripPromptContext(text string) string {
	if strings.Contains(text, "<system-reminder") {
		text = codebuddySystemReminder.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(codebuddyUserQueryTags.ReplaceAllString(text, "$1"))
}

// codebuddyStructuredInput reads the input that a block records for a command,
// a shell input or a memory input, and reports whether the block holds one
// (parseInput in 2.160.0). A command gives its name, or its message when it has
// no name, followed by its arguments.
func codebuddyStructuredInput(text string) (string, bool) {
	if !strings.Contains(text, "<") {
		return "", false
	}
	if strings.Contains(text, "<bash-input>") {
		if input := codebuddyXMLContent(text, "bash-input"); input != "" {
			return input, true
		}
	}
	if strings.Contains(text, "<user-memory-input>") || strings.Contains(text, "<user-memory-scope>") {
		if input := cmp.Or(codebuddyXMLContent(text, "user-memory-input"), codebuddyXMLContent(text, "user-memory-scope")); input != "" {
			return input, true
		}
	}
	if strings.Contains(text, "<command-name>") || strings.Contains(text, "<command-message>") || strings.Contains(text, "<command-args>") {
		command := cmp.Or(codebuddyXMLContent(text, "command-name"), codebuddyXMLContent(text, "command-message"))
		if command == "" {
			return "", false
		}
		return command + " " + codebuddyXMLContent(text, "command-args"), true
	}
	return "", false
}

// codebuddyXMLContent returns the text of the first element of tag in text, or
// "" when text holds none (getXmlContent in 2.160.0).
func codebuddyXMLContent(text, tag string) string {
	open, closing := "<"+tag+">", "</"+tag+">"
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], closing)
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}
