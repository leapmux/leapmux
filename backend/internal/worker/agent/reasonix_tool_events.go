package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

type reasonixToolEvent struct {
	Schema         int                          `json:"schema_version"`
	Type           string                       `json:"type"`
	ID             string                       `json:"id"`
	Parent         string                       `json:"parent"`
	Head           string                       `json:"head"`
	At             time.Time                    `json:"at"`
	Messages       []json.RawMessage            `json:"msgs"`
	LegacyMessages []json.RawMessage            `json:"messages"`
	MessageIndex   int                          `json:"message_index"`
	Target         string                       `json:"target"`
	Targets        map[string][]json.RawMessage `json:"targets"`
	NewHead        string                       `json:"new_head"`
	From           string                       `json:"from"`
	To             string                       `json:"to"`
}

// reasonixMessageLocation states where one message sits in the event log: the
// line that carries it, and which message of that line it is.
//
// The graph keeps a location rather than the message itself. The log of a long
// session holds every tool result that session produced, so a graph that kept
// those bodies would grow with the session. A location costs the same for every
// message, and records() reads back only the lines a waiting tool call needs.
type reasonixMessageLocation struct {
	offset int64
	length int
	// key selects one entry of the line's `targets` map, and fromTargets states
	// that the key applies. The two are separate so a message that carries no
	// key is never read as the `targets` entry of the empty id.
	key         string
	fromTargets bool
	index       int
}

type reasonixToolNode struct {
	parent string
	toolID string
	at     reasonixMessageLocation
}

type reasonixToolHead struct {
	leaf    string
	at      time.Time
	order   int
	retired bool
}

// Keep graph links for all messages, and locate the bodies of the tool results.
type reasonixToolGraph struct {
	nodes      map[string]reasonixToolNode
	heads      map[string]*reasonixToolHead
	headOrder  []string
	redactions map[string]reasonixToolNode
	selected   string
	order      int
}

func newReasonixToolGraph() *reasonixToolGraph {
	graph := &reasonixToolGraph{
		nodes:      make(map[string]reasonixToolNode),
		heads:      make(map[string]*reasonixToolHead),
		redactions: make(map[string]reasonixToolNode),
	}
	graph.head(reasonixDefaultHead)
	return graph
}

// reasonixDefaultHead is the branch an event that states no head belongs to.
const reasonixDefaultHead = "main"

// normalizeReasonixHeadID gives the default branch its name.
//
// Reasonix omits the head of the main branch, and every lookup in this file keys
// on the name. A raw "" stored as the selection found no head and dropped the
// reader onto the newest branch instead of the one the session selected.
func normalizeReasonixHeadID(id string) string {
	if id == "" {
		return reasonixDefaultHead
	}
	return id
}

func (g *reasonixToolGraph) head(id string) *reasonixToolHead {
	id = normalizeReasonixHeadID(id)
	h := g.heads[id]
	if h == nil {
		h = &reasonixToolHead{}
		g.heads[id] = h
		g.headOrder = append(g.headOrder, id)
	}
	return h
}

func (g *reasonixToolGraph) body(messages []json.RawMessage, at reasonixMessageLocation) (reasonixToolNode, error) {
	if len(messages) != 1 {
		return reasonixToolNode{}, fmt.Errorf("a Reasonix event must contain one message")
	}
	return reasonixToolNode{toolID: reasonixToolCallID(messages[0]), at: at}, nil
}

func (g *reasonixToolGraph) apply(event reasonixToolEvent, at reasonixMessageLocation) error {
	g.order++
	switch event.Type {
	case "message":
		if event.ID == "" {
			return fmt.Errorf("a Reasonix message event has no ID")
		}
		if _, exists := g.nodes[event.ID]; exists {
			return nil
		}
		node, err := g.body(event.Messages, at)
		if err != nil {
			return err
		}
		node.parent = event.Parent
		g.nodes[event.ID] = node
		head := g.head(event.Head)
		head.leaf, head.at, head.order = event.ID, event.At, g.order
	case "patch":
		node, exists := g.nodes[event.Target]
		if !exists {
			return nil
		}
		updated, err := g.body(event.Messages, at)
		if err != nil {
			return err
		}
		updated.parent = node.parent
		g.nodes[event.Target] = updated
	case "redact":
		for id, messages := range event.Targets {
			target := at
			target.key, target.fromTargets = id, true
			updated, err := g.body(messages, target)
			if err != nil {
				return err
			}
			g.redactions[id] = updated
		}
	case "fork":
		if event.NewHead == "" {
			return fmt.Errorf("a Reasonix fork event has no new head")
		}
		if _, exists := g.heads[event.NewHead]; exists {
			return nil
		}
		g.head(event.Head)
		head := g.head(event.NewHead)
		head.leaf, head.at, head.order = event.From, event.At, g.order
	case "rewind":
		head := g.head(event.Head)
		head.leaf, head.at, head.order = event.To, event.At, g.order
	case "select":
		g.selected = normalizeReasonixHeadID(event.Head)
	case "retire":
		g.head(event.Head).retired = true
	case "system":
		if _, err := g.body(event.Messages, at); err != nil {
			return err
		}
		g.head(event.Head)
	case "rename", "turn_begin", "turn_end", "compaction":
		g.head(event.Head)
	case "log", "writer", "checkpoint":
		// These records do not change the tool results on a session branch.
	default:
		// A Reasonix build that adds an event type must not cost the reader every
		// tool result in the session. A skipped event can leave this branch view
		// stale, which is the smaller failure: the alternative records nothing.
		slog.Debug("Skip an unsupported Reasonix event type", "schema", 2, "type", event.Type)
	}
	return nil
}

func (g *reasonixToolGraph) selectedHead() *reasonixToolHead {
	if head := g.heads[g.selected]; head != nil && !head.retired {
		return head
	}
	for _, includeRetired := range []bool{false, true} {
		var best *reasonixToolHead
		for _, id := range g.headOrder {
			head := g.heads[id]
			if head.retired && !includeRetired {
				continue
			}
			if best == nil || head.at.After(best.at) || (head.at.Equal(best.at) && head.order > best.order) {
				best = head
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// records reads back the messages the pending tool calls wait for.
//
// It walks the selected branch from its leaf and reads one line for each tool
// call it answers, so a session with a thousand messages and two pending calls
// reads two lines.
func (g *reasonixToolGraph) records(file *os.File, pending map[string][]byte) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	head := g.selectedHead()
	if head == nil {
		return out, nil
	}
	seen := make(map[string]struct{})
	for id := head.leaf; id != ""; {
		if _, exists := seen[id]; exists {
			// A cycle describes no branch, so nothing on it is a record of the
			// session. This is not the "one unreadable event" case, where the rest
			// of the branch still stands.
			return nil, fmt.Errorf("the Reasonix session graph contains a cycle")
		}
		seen[id] = struct{}{}
		node, exists := g.nodes[id]
		if !exists {
			break
		}
		parent := node.parent
		if redacted, exists := g.redactions[id]; exists {
			node = redacted
		}
		id = parent
		if node.toolID == "" {
			continue
		}
		if _, wanted := pending[node.toolID]; !wanted {
			continue
		}
		if _, found := out[node.toolID]; found {
			continue
		}
		raw, err := readReasonixMessageAt(file, node.at)
		// A log that Reasonix rewrote in place leaves the stored offsets pointing
		// at other bytes. The tool-call id of what comes back is what states that
		// the line is still the one the graph recorded.
		if err != nil || reasonixToolCallID(raw) != node.toolID {
			continue
		}
		out[node.toolID] = raw
	}
	return out, nil
}

// readReasonixMessageAt reads one message back out of the line that carries it.
func readReasonixMessageAt(file *os.File, at reasonixMessageLocation) (json.RawMessage, error) {
	if at.length <= 0 || at.length > liveStdoutMaxTokenSize() {
		return nil, fmt.Errorf("a Reasonix event line has an unusable length")
	}
	line := make([]byte, at.length)
	if _, err := file.ReadAt(line, at.offset); err != nil {
		return nil, err
	}
	var event struct {
		Messages       []json.RawMessage            `json:"msgs"`
		LegacyMessages []json.RawMessage            `json:"messages"`
		Targets        map[string][]json.RawMessage `json:"targets"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return nil, err
	}
	messages := event.Messages
	switch {
	case at.fromTargets:
		messages = event.Targets[at.key]
	case len(messages) == 0:
		messages = event.LegacyMessages
	}
	if at.index < 0 || at.index >= len(messages) {
		return nil, fmt.Errorf("a Reasonix event line no longer holds message %d", at.index)
	}
	return messages[at.index], nil
}

// reasonixEventCache keeps one event log's branch graph between reads.
//
// The transcript reads the log once for every agent message while a tool result
// waits for its record, and Reasonix only APPENDS to that log. So a read applies
// the lines after the offset the previous read consumed rather than the whole
// file again, which is what kept a long session from enriching at all: the
// replay outgrew the reader's time budget and each read then returned nothing.
//
// The cache resets when the log moves, changes identity, or shrinks, because
// none of the stored offsets describe the new bytes.
type reasonixEventCache struct {
	path   string
	file   os.FileInfo
	offset int64
	graph  *reasonixToolGraph
	legacy map[string]reasonixMessageLocation
	schema int
	count  int
}

func (c *reasonixEventCache) resetTo(path string, info os.FileInfo) {
	c.path = path
	c.file = info
	c.offset = 0
	c.graph = newReasonixToolGraph()
	c.legacy = make(map[string]reasonixMessageLocation)
	c.schema = 0
	c.count = 0
}

// read applies the lines the previous read left, then answers the pending calls.
func (c *reasonixEventCache) read(ctx context.Context, path string, file *os.File, pending map[string][]byte) (map[string]json.RawMessage, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Reasonix transcript is not a regular file")
	}
	if c.graph == nil || c.path != path || c.file == nil || !os.SameFile(c.file, info) || info.Size() < c.offset {
		c.resetTo(path, info)
	}
	c.file = info
	offset, readErr := readReasonixJSONL(ctx, file, c.offset, info.Size(), func(raw json.RawMessage, lineOffset int64, length int) error {
		return c.applyLine(raw, lineOffset, length)
	})
	c.offset = offset
	// Answer with what the events so far describe, even when one of them failed.
	// The caller logs the error and still enriches the calls the graph did place.
	if c.schema == 2 {
		records, err := c.graph.records(file, pending)
		return records, errors.Join(readErr, err)
	}
	out := make(map[string]json.RawMessage, len(c.legacy))
	for id, at := range c.legacy {
		if _, wanted := pending[id]; !wanted {
			continue
		}
		raw, err := readReasonixMessageAt(file, at)
		if err != nil || reasonixToolCallID(raw) != id {
			continue
		}
		out[id] = raw
	}
	return out, readErr
}

func (c *reasonixEventCache) applyLine(raw json.RawMessage, offset int64, length int) error {
	var event reasonixToolEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if c.schema == 0 {
		c.schema = event.Schema
	}
	if event.Schema != c.schema {
		return fmt.Errorf("the Reasonix event log changes schema within one file")
	}
	at := reasonixMessageLocation{offset: offset, length: length}
	switch c.schema {
	case 2:
		return c.graph.apply(event, at)
	case 1:
		switch event.Type {
		case "replace":
			clear(c.legacy)
			c.count = 0
		case "append":
			if event.MessageIndex != c.count {
				return fmt.Errorf("the Reasonix append position does not match its history")
			}
		default:
			// Same trade-off as the schema-2 reader above: one unknown event type
			// must not cost the reader every tool result in the session.
			slog.Debug("Skip an unsupported Reasonix event type", "schema", c.schema, "type", event.Type)
			return nil
		}
		for index, message := range event.LegacyMessages {
			if id := reasonixToolCallID(message); id != "" {
				entry := at
				entry.index = index
				c.legacy[id] = entry
			}
		}
		c.count += len(event.LegacyMessages)
		return nil
	default:
		return fmt.Errorf("unsupported Reasonix event schema %d", c.schema)
	}
}
