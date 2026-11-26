package grok

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

const grokUpdatesJournal = "updates.jsonl"
const grokUpdatesJournalLimit = 64 << 20

func sameGrokFileSnapshot(before, after os.FileInfo) bool {
	return before.Mode().IsRegular() && after.Mode().IsRegular() && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

// readGrokJournalBaseline computes the baseline hash of the complete newline-
// terminated prefix of updates.jsonl by streaming in chunks instead of loading
// up to grokUpdatesJournalLimit bytes into memory.
func readGrokJournalBaseline(home, group, sessionID string) (baseline sessionstore.JournalCursor, err error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return sessionstore.JournalCursor{}, fmt.Errorf("the Grok session ID is invalid: %w", err)
	}
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	defer func() { _ = root.Close() }()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "sessions", group, sessionID)
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			baseline, err = sessionstore.JournalCursor{}, errors.Join(err, closeErr)
		}
	}()
	dir := chain.Root()
	checked, err := dir.Lstat(grokUpdatesJournal)
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	if checked.Mode()&os.ModeSymlink != 0 || !checked.Mode().IsRegular() || checked.Size() < 0 || checked.Size() > grokUpdatesJournalLimit {
		return sessionstore.JournalCursor{}, sessionstore.ErrArchiveFileModeOrSize
	}
	file, err := dir.Open(grokUpdatesJournal)
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			baseline, err = sessionstore.JournalCursor{}, errors.Join(err, closeErr)
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	if !sameGrokFileSnapshot(checked, opened) {
		return sessionstore.JournalCursor{}, errors.New("the Grok journal changed before it opened")
	}

	cursor, totalRead, err := sessionstore.ReadJournalCursor(file, opened.Size())
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	if totalRead != opened.Size() {
		return sessionstore.JournalCursor{}, errors.New("the Grok journal size changed during baseline read")
	}
	after, err := file.Stat()
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	current, err := dir.Lstat(grokUpdatesJournal)
	if err != nil {
		return sessionstore.JournalCursor{}, err
	}
	if !sameGrokFileSnapshot(opened, after) || !sameGrokFileSnapshot(after, current) {
		return sessionstore.JournalCursor{}, errors.New("the Grok journal changed during baseline read")
	}
	return cursor, nil
}

type grokConsumption struct {
	payload   grokInterjectionPayload
	eventTime int64
	eventSeq  uint64
}

type grokInterjectionPayload struct {
	text   string
	images []grokInterjectionImage
}

type grokInterjectionImage struct {
	mimeType, filename string
	digest             [sha256.Size]byte
}

func grokImageFingerprint(mimeType, filename string, data []byte) grokInterjectionImage {
	return grokInterjectionImage{mimeType: mimeType, filename: filename, digest: sha256.Sum256(data)}
}

type grokJournalScope struct {
	sessionID, predecessorID, promptID string
	childMessages                      bool
}

type grokConsumptionParser struct {
	scope          grokJournalScope
	consumed       []grokConsumption
	predecessorEnd uint64
	promptIndex    *int
	done           bool
}

func (p *grokConsumptionParser) parseComplete(lines []byte) bool {
	sessionID, predecessorID := p.scope.sessionID, p.scope.predecessorID
	tracksPrompt := predecessorID != "" || p.scope.childMessages
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		if p.done {
			return true
		}
		if len(line) == 0 {
			continue
		}
		if !bytes.Contains(line, []byte(`"interjection"`)) &&
			(!tracksPrompt || !bytes.Contains(line, []byte(`"user_message_chunk"`)) && !bytes.Contains(line, []byte(`"turn_completed"`))) {
			continue
		}
		var envelope struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			return false
		}
		if envelope.Method != "session/update" && (!tracksPrompt || envelope.Method != grokSessionUpdateMethod) {
			continue
		}
		var header struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind string `json:"sessionUpdate"`
			} `json:"update"`
		}
		if json.Unmarshal(envelope.Params, &header) != nil {
			continue
		}
		if envelope.Method == grokSessionUpdateMethod {
			if !tracksPrompt || header.SessionID != sessionID || header.Update.Kind != contracts.GrokNotificationTurnCompleted {
				continue
			}
			var completed struct {
				Meta struct {
					EventID string `json:"eventId"`
				} `json:"_meta"`
				Update struct {
					PromptID string `json:"prompt_id"`
				} `json:"update"`
			}
			if json.Unmarshal(envelope.Params, &completed) != nil {
				return false
			}
			if completed.Update.PromptID == p.scope.promptID && (p.predecessorEnd != 0 || p.scope.childMessages) {
				p.done = true
				return true
			}
			if predecessorID != "" && completed.Update.PromptID == predecessorID {
				sequence, ok := grokEventSequence(sessionID, completed.Meta.EventID)
				if !ok || p.predecessorEnd != 0 {
					return false
				}
				p.predecessorEnd = sequence
			}
			continue
		}
		if header.Update.Kind != contracts.ACPUpdateUserMessageChunk ||
			predecessorID != "" && p.predecessorEnd == 0 {
			continue
		}
		var params struct {
			SessionID string `json:"sessionId"`
			Meta      struct {
				EventID string `json:"eventId"`
				Time    int64  `json:"agentTimestampMs"`
			} `json:"_meta"`
			Update struct {
				Meta struct {
					Interjection bool   `json:"interjection"`
					PromptIndex  *int   `json:"promptIndex"`
					ModelID      string `json:"modelId"`
				} `json:"_meta"`
				Content struct {
					Type     string  `json:"type"`
					Text     *string `json:"text"`
					MIMEType string  `json:"mimeType"`
					URI      string  `json:"uri"`
					Data     []byte  `json:"data"`
					Meta     struct {
						DisplayText *string `json:"displayText"`
					} `json:"_meta"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(envelope.Params, &params) != nil {
			return false
		}
		ordinaryPrompt := !params.Update.Meta.Interjection
		childMessage := p.scope.childMessages && ordinaryPrompt && params.Update.Meta.PromptIndex == nil && params.Update.Meta.ModelID != ""
		if !childMessage && (p.scope.childMessages || ordinaryPrompt && (predecessorID == "" || params.Update.Meta.PromptIndex == nil)) {
			continue
		}
		seq, ok := grokEventSequence(sessionID, params.Meta.EventID)
		if params.SessionID != sessionID || !ok || seq <= p.predecessorEnd || params.Meta.Time <= 0 {
			return false
		}
		content := params.Update.Content
		var last *grokConsumption
		if len(p.consumed) != 0 {
			last = &p.consumed[len(p.consumed)-1]
		}
		if childMessage {
			// A batch of child messages can write several text blocks under
			// one event ID. Preserve every block in its journal order.
			if content.Type != "text" || content.Text == nil || *content.Text == "" ||
				last != nil && (seq < last.eventSeq || params.Meta.Time < last.eventTime ||
					seq == last.eventSeq && params.Meta.Time != last.eventTime) {
				return false
			}
			p.consumed = append(p.consumed, grokConsumption{
				payload: grokInterjectionPayload{text: *content.Text}, eventTime: params.Meta.Time, eventSeq: seq,
			})
			continue
		}
		if predecessorID != "" {
			if ordinaryPrompt {
				// The first ordinary user event after the predecessor is the
				// successor input. Its image parts share its prompt index.
				if p.promptIndex == nil {
					if last != nil {
						return false
					}
					p.promptIndex = params.Update.Meta.PromptIndex
				} else if *p.promptIndex != *params.Update.Meta.PromptIndex {
					p.done = true
					return true
				} else if len(p.consumed) != 1 || seq <= last.eventSeq {
					return false
				}
			} else if p.promptIndex == nil {
				return false
			}
		}
		switch content.Type {
		case "text":
			// The text record starts the event even when its display text is empty.
			text := content.Meta.DisplayText
			if ordinaryPrompt {
				if last != nil {
					return false
				}
				text = content.Text
			}
			if text == nil || last != nil && (seq <= last.eventSeq || params.Meta.Time < last.eventTime) {
				return false
			}
			p.consumed = append(p.consumed, grokConsumption{
				payload: grokInterjectionPayload{text: *text}, eventTime: params.Meta.Time, eventSeq: seq,
			})
		case "image":
			// An interjection's parts share its event ID. An ordinary prompt's
			// parts share its prompt index and use increasing event IDs.
			if ordinaryPrompt && last == nil {
				p.consumed = append(p.consumed, grokConsumption{eventTime: params.Meta.Time, eventSeq: seq})
				last = &p.consumed[len(p.consumed)-1]
			}
			if last == nil || !ordinaryPrompt && last.eventSeq != seq || last.eventTime != params.Meta.Time ||
				content.MIMEType == "" || len(content.Data) == 0 {
				return false
			}
			last.payload.images = append(last.payload.images, grokImageFingerprint(content.MIMEType, content.URI, content.Data))
			last.eventSeq = seq
		default:
			return false
		}
	}
	return true
}

func readGrokConsumptions(baseline sessionstore.JournalCursor, data []byte, scope grokJournalScope) ([]grokConsumption, bool) {
	appended, ok := baseline.AppendedBytes(data)
	if !ok {
		return nil, false
	}
	parser := grokConsumptionParser{scope: scope}
	if !parser.parseComplete(appended) {
		return nil, false
	}
	return parser.consumed, true
}

var errGrokJournalInvalid = errors.New("the Grok journal is invalid")

type grokJournalPoller struct {
	home           string
	group          string
	baseline       sessionstore.JournalCursor
	parser         grokConsumptionParser
	lastInfo       os.FileInfo
	parsedOffset   int64
	verifiedPrefix bool
}

func newGrokJournalPoller(home, group string, baseline sessionstore.JournalCursor, scope grokJournalScope) *grokJournalPoller {
	return &grokJournalPoller{
		home:     home,
		group:    group,
		baseline: baseline,
		parser:   grokConsumptionParser{scope: scope},
	}
}

// poll checks updates.jsonl and parses only newly appended complete lines.
// Unchanged polls return (rows, false, nil) after a single Lstat without
// reading or re-hashing the file.
func (p *grokJournalPoller) poll() (rows []grokConsumption, changed bool, err error) {
	if _, err := uuid.Parse(p.parser.scope.sessionID); err != nil {
		return nil, false, err
	}
	root, err := sessionstore.OpenArchiveRoot(p.home)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = root.Close() }()
	chain, err := sessionstore.OpenCheckedArchiveDirectoryChain(root, "sessions", p.group, p.parser.scope.sessionID)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := chain.Close(); closeErr != nil {
			rows, changed, err = nil, false, errors.Join(err, closeErr)
		}
	}()
	dir := chain.Root()
	checked, err := dir.Lstat(grokUpdatesJournal)
	if err != nil {
		return nil, false, err
	}
	if checked.Mode()&os.ModeSymlink != 0 || !checked.Mode().IsRegular() || checked.Size() < 0 || checked.Size() > grokUpdatesJournalLimit {
		return nil, false, sessionstore.ErrArchiveFileModeOrSize
	}
	if p.lastInfo != nil && sameGrokFileSnapshot(p.lastInfo, checked) {
		return p.parser.consumed, false, nil
	}
	file, err := dir.Open(grokUpdatesJournal)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			rows, changed, err = nil, false, errors.Join(err, closeErr)
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !sameGrokFileSnapshot(checked, opened) {
		return nil, false, errors.New("the Grok journal changed before it opened")
	}

	fullRescan := !p.verifiedPrefix || p.lastInfo == nil || !os.SameFile(p.lastInfo, opened) || opened.Size() < p.parsedOffset
	var startOffset int64
	if fullRescan {
		if opened.Size() < p.baseline.Size() {
			return nil, false, errGrokJournalInvalid
		}
		matched, err := p.baseline.VerifyPrefix(file)
		if err != nil {
			return nil, false, err
		}
		if !matched {
			return nil, false, errGrokJournalInvalid
		}
		startOffset = p.baseline.Size()
	} else {
		startOffset = p.parsedOffset
		if _, err := file.Seek(startOffset, io.SeekStart); err != nil {
			return nil, false, err
		}
	}

	remaining := opened.Size() - startOffset
	var chunk []byte
	if remaining > 0 {
		chunk = make([]byte, remaining)
		if _, err := io.ReadFull(file, chunk); err != nil {
			return nil, false, err
		}
	}
	after, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	current, err := dir.Lstat(grokUpdatesJournal)
	if err != nil {
		return nil, false, err
	}
	if !sameGrokFileSnapshot(opened, after) || !sameGrokFileSnapshot(after, current) {
		return nil, false, errors.New("the Grok journal changed during the read")
	}

	if fullRescan {
		p.parser = grokConsumptionParser{scope: p.parser.scope}
		p.parsedOffset = startOffset
		p.verifiedPrefix = true
	}
	if lastNL := bytes.LastIndexByte(chunk, '\n'); lastNL >= 0 {
		if !p.parser.parseComplete(chunk[:lastNL+1]) {
			return nil, false, errGrokJournalInvalid
		}
		p.parsedOffset = startOffset + int64(lastNL+1)
	}
	p.lastInfo = after
	return p.parser.consumed, true, nil
}

func grokEventSequence(sessionID, eventID string) (uint64, bool) {
	suffix, ok := strings.CutPrefix(eventID, sessionID+"-")
	seq, err := strconv.ParseUint(suffix, 10, 64)
	return seq, ok && err == nil && seq > 0
}

type grokSteerAdmission struct {
	id          string
	payload     grokInterjectionPayload
	echoed      bool
	afterStream int64
}

type grokSteerStream struct {
	start    int64
	eventSeq uint64
	keys     []string
}

var errGrokConsumptionIncomplete = errors.New("the Grok journal has not proved the steer batch")

// matchGrokConsumptions proves a complete batch at each observed model boundary.
// A proper prefix cannot identify one of several identical accepted inputs.
func matchGrokConsumptions(inputs []grokSteerAdmission, streams []grokSteerStream, rows []grokConsumption) (map[string]int, error) {
	result := make(map[string]int)
	var inputIndex, rowIndex int
	var previousStart int64
	var previousSeq uint64
	for index, stream := range streams {
		if stream.start <= previousStart || stream.eventSeq <= previousSeq {
			return nil, errors.New("the Grok model boundaries are out of order")
		}
		previousStart, previousSeq = stream.start, stream.eventSeq
		firstInput, firstRow := inputIndex, rowIndex
		for inputIndex < len(inputs) && inputs[inputIndex].echoed && inputs[inputIndex].afterStream < stream.start {
			inputIndex++
		}
		for rowIndex < len(rows) && rows[rowIndex].eventSeq < stream.eventSeq && rows[rowIndex].eventTime <= stream.start {
			rowIndex++
		}
		if inputIndex-firstInput != rowIndex-firstRow {
			return nil, errGrokConsumptionIncomplete
		}
		for i := firstInput; i < inputIndex; i++ {
			input := inputs[i]
			row := firstRow + i - firstInput
			expected, actual := input.payload, rows[row].payload
			count := len(actual.images)
			if input.id == "" || expected.text == "" && len(expected.images) == 0 ||
				expected.text != actual.text || count > len(expected.images) ||
				!slices.Equal(expected.images[:count], actual.images) {
				return nil, errors.New("the Grok journal and steer batch differ")
			}
			if count < len(expected.images) {
				if row == len(rows)-1 {
					return nil, errGrokConsumptionIncomplete
				}
				return nil, errors.New("the Grok journal skipped part of a steer")
			}
			result[input.id] = index
		}
	}
	return result, nil
}

const grokJournalReadInterval = 25 * time.Millisecond
