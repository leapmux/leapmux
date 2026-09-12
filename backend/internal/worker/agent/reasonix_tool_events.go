package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

type reasonixToolNode struct {
	parent string
	toolID string
	record json.RawMessage
}

type reasonixToolHead struct {
	leaf    string
	at      time.Time
	order   int
	retired bool
}

// Keep graph links for all messages, but retain bodies only for requested tool results.
type reasonixToolGraph struct {
	nodes      map[string]reasonixToolNode
	heads      map[string]*reasonixToolHead
	headOrder  []string
	redactions map[string]reasonixToolNode
	selected   string
	pending    map[string][]byte
	order      int
}

func (g *reasonixToolGraph) head(id string) *reasonixToolHead {
	if id == "" {
		id = "main"
	}
	h := g.heads[id]
	if h == nil {
		h = &reasonixToolHead{}
		g.heads[id] = h
		g.headOrder = append(g.headOrder, id)
	}
	return h
}

func (g *reasonixToolGraph) body(messages []json.RawMessage) (reasonixToolNode, error) {
	if len(messages) != 1 {
		return reasonixToolNode{}, fmt.Errorf("a Reasonix event must contain one message")
	}
	id, raw := reasonixSelectedRecord(messages[0], g.pending)
	return reasonixToolNode{toolID: id, record: raw}, nil
}

func (g *reasonixToolGraph) apply(event reasonixToolEvent) error {
	g.order++
	switch event.Type {
	case "message":
		if event.ID == "" {
			return fmt.Errorf("a Reasonix message event has no ID")
		}
		if _, exists := g.nodes[event.ID]; exists {
			return nil
		}
		node, err := g.body(event.Messages)
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
		updated, err := g.body(event.Messages)
		if err != nil {
			return err
		}
		updated.parent = node.parent
		g.nodes[event.Target] = updated
	case "redact":
		for id, messages := range event.Targets {
			updated, err := g.body(messages)
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
		g.selected = event.Head
	case "retire":
		g.head(event.Head).retired = true
	case "system":
		if _, err := g.body(event.Messages); err != nil {
			return err
		}
		g.head(event.Head)
	case "rename", "turn_begin", "turn_end", "compaction":
		g.head(event.Head)
	case "log", "writer", "checkpoint":
		// These records do not change the tool results on a session branch.
	default:
		return fmt.Errorf("unsupported Reasonix event type %q", event.Type)
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

func (g *reasonixToolGraph) records() (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage)
	head := g.selectedHead()
	if head == nil {
		return out, nil
	}
	seen := make(map[string]struct{})
	for id := head.leaf; id != ""; {
		if _, exists := seen[id]; exists {
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
		if node.toolID != "" {
			if _, found := out[node.toolID]; !found {
				out[node.toolID] = node.record
			}
		}
		id = parent
	}
	return out, nil
}

func readReasonixToolEvents(ctx context.Context, file *os.File, pending map[string][]byte) (map[string]json.RawMessage, error) {
	graph := &reasonixToolGraph{
		nodes:      make(map[string]reasonixToolNode),
		heads:      make(map[string]*reasonixToolHead),
		redactions: make(map[string]reasonixToolNode),
		pending:    pending,
	}
	graph.head("main")
	legacy := make(map[string]json.RawMessage)
	count := 0
	schema := 0
	err := readReasonixJSONL(ctx, file, func(raw json.RawMessage) error {
		var event reasonixToolEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		if schema == 0 {
			schema = event.Schema
		}
		if event.Schema != schema {
			return fmt.Errorf("the Reasonix event log changes schema within one file")
		}
		switch schema {
		case 2:
			return graph.apply(event)
		case 1:
			switch event.Type {
			case "replace":
				clear(legacy)
				count = 0
			case "append":
				if event.MessageIndex != count {
					return fmt.Errorf("the Reasonix append position does not match its history")
				}
			default:
				return fmt.Errorf("unsupported Reasonix event type %q", event.Type)
			}
			for _, message := range event.LegacyMessages {
				id, record := reasonixSelectedRecord(message, pending)
				if id != "" {
					legacy[id] = record
				}
			}
			count += len(event.LegacyMessages)
			return nil
		default:
			return fmt.Errorf("unsupported Reasonix event schema %d", schema)
		}
	})
	if err != nil {
		return nil, err
	}
	if schema == 2 {
		return graph.records()
	}
	return legacy, nil
}
