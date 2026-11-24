package grok

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

// grokJournalBaseline identifies the complete prefix before the first steer.
// Grok appends updates independently of its live transport.
type grokJournalBaseline struct {
	size int
	hash [sha256.Size]byte
}

func grokJournalPrefix(data []byte) []byte {
	return data[:bytes.LastIndexByte(data, '\n')+1]
}

func grokBaseline(data []byte) grokJournalBaseline {
	prefix := grokJournalPrefix(data)
	return grokJournalBaseline{size: len(prefix), hash: sha256.Sum256(prefix)}
}

func readGrokUpdates(home, group, sessionID string) ([]byte, error) {
	if _, err := uuid.Parse(sessionID); err != nil {
		return nil, fmt.Errorf("the Grok session ID is invalid: %w", err)
	}
	root, err := sessionstore.OpenArchiveRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return sessionstore.ReadRegularFileWithoutSymlinkAncestors(root, grokUpdatesJournalLimit, "sessions", group, sessionID, grokUpdatesJournal)
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

func readGrokConsumptions(baseline grokJournalBaseline, data []byte, scope grokJournalScope) ([]grokConsumption, bool) {
	sessionID, predecessorID := scope.sessionID, scope.predecessorID
	tracksPrompt := predecessorID != "" || scope.childMessages
	complete := grokJournalPrefix(data)
	if len(complete) < baseline.size || sha256.Sum256(complete[:baseline.size]) != baseline.hash {
		return nil, false
	}
	var consumed []grokConsumption
	var predecessorEnd uint64
	var promptIndex *int
	for line := range bytes.SplitSeq(complete[baseline.size:], []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"interjection"`)) &&
			(!tracksPrompt || !bytes.Contains(line, []byte(`"user_message_chunk"`)) && !bytes.Contains(line, []byte(`"turn_completed"`))) {
			continue
		}
		var row struct {
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Meta      struct {
					EventID string `json:"eventId"`
					Time    int64  `json:"agentTimestampMs"`
				} `json:"_meta"`
				Update struct {
					Kind     string `json:"sessionUpdate"`
					PromptID string `json:"prompt_id"`
					Meta     struct {
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
			} `json:"params"`
		}
		if json.Unmarshal(line, &row) != nil {
			return nil, false
		}
		params := row.Params
		if tracksPrompt && row.Method == grokSessionUpdateMethod &&
			params.SessionID == sessionID && params.Update.Kind == contracts.GrokNotificationTurnCompleted {
			if params.Update.PromptID == scope.promptID && (predecessorEnd != 0 || scope.childMessages) {
				break
			}
			if predecessorID != "" && params.Update.PromptID == predecessorID {
				sequence, ok := grokEventSequence(sessionID, params.Meta.EventID)
				if !ok || predecessorEnd != 0 {
					return nil, false
				}
				predecessorEnd = sequence
			}
			continue
		}
		if row.Method != "session/update" || params.Update.Kind != contracts.ACPUpdateUserMessageChunk ||
			predecessorID != "" && predecessorEnd == 0 {
			continue
		}
		ordinaryPrompt := !params.Update.Meta.Interjection
		childMessage := scope.childMessages && ordinaryPrompt && params.Update.Meta.PromptIndex == nil && params.Update.Meta.ModelID != ""
		if !childMessage && (scope.childMessages || ordinaryPrompt && (predecessorID == "" || params.Update.Meta.PromptIndex == nil)) {
			continue
		}
		seq, ok := grokEventSequence(sessionID, params.Meta.EventID)
		if params.SessionID != sessionID || !ok || seq <= predecessorEnd || params.Meta.Time <= 0 {
			return nil, false
		}
		content := params.Update.Content
		var last *grokConsumption
		if len(consumed) != 0 {
			last = &consumed[len(consumed)-1]
		}
		if childMessage {
			// A batch of child messages can write several text blocks under
			// one event ID. Preserve every block in its journal order.
			if content.Type != "text" || content.Text == nil || *content.Text == "" ||
				last != nil && (seq < last.eventSeq || params.Meta.Time < last.eventTime ||
					seq == last.eventSeq && params.Meta.Time != last.eventTime) {
				return nil, false
			}
			consumed = append(consumed, grokConsumption{
				payload: grokInterjectionPayload{text: *content.Text}, eventTime: params.Meta.Time, eventSeq: seq,
			})
			continue
		}
		if predecessorID != "" {
			if ordinaryPrompt {
				// The first ordinary user event after the predecessor is the
				// successor input. Its image parts share its prompt index.
				if promptIndex == nil {
					if last != nil {
						return nil, false
					}
					promptIndex = params.Update.Meta.PromptIndex
				} else if *promptIndex != *params.Update.Meta.PromptIndex {
					break
				} else if len(consumed) != 1 || seq <= last.eventSeq {
					return nil, false
				}
			} else if promptIndex == nil {
				return nil, false
			}
		}
		switch content.Type {
		case "text":
			// The text record starts the event even when its display text is empty.
			text := content.Meta.DisplayText
			if ordinaryPrompt {
				if last != nil {
					return nil, false
				}
				text = content.Text
			}
			if text == nil || last != nil && (seq <= last.eventSeq || params.Meta.Time < last.eventTime) {
				return nil, false
			}
			consumed = append(consumed, grokConsumption{
				payload: grokInterjectionPayload{text: *text}, eventTime: params.Meta.Time, eventSeq: seq,
			})
		case "image":
			// An interjection's parts share its event ID. An ordinary prompt's
			// parts share its prompt index and use increasing event IDs.
			if ordinaryPrompt && last == nil {
				consumed = append(consumed, grokConsumption{eventTime: params.Meta.Time, eventSeq: seq})
				last = &consumed[len(consumed)-1]
			}
			if last == nil || !ordinaryPrompt && last.eventSeq != seq || last.eventTime != params.Meta.Time ||
				content.MIMEType == "" || len(content.Data) == 0 {
				return nil, false
			}
			last.payload.images = append(last.payload.images, grokImageFingerprint(content.MIMEType, content.URI, content.Data))
			last.eventSeq = seq
		default:
			return nil, false
		}
	}
	return consumed, true
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
