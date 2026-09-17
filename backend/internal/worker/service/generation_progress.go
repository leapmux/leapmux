package service

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const generationProgressInterval = 250 * time.Millisecond

// How often a RUNNING tool's output tail reaches the browser.
//
// Slower than the counter above on purpose. A counter is one number and the card
// animates it; a tail is text that re-lays the row out, and a shell command that
// prints a line per millisecond would otherwise make the transcript jump.
const runningToolTailInterval = time.Second

// The longest tail one running tool broadcasts, in bytes.
//
// The card draws the LAST lines, so a longer tail buys nothing a reader sees and
// every byte of it rides an ephemeral broadcast on every tick. The finished row
// carries the whole output through the provider's own retained frame.
const runningToolTailBytes = 2048

var generationProgressRevision atomic.Uint64

type progressEmission struct {
	info map[string]interface{}
}

// The live output of ONE running tool call, between two broadcasts.
type runningToolTail struct {
	text string
	// truncated states that output was lost BEFORE this text: the provider said
	// so, or the cap below dropped the head of what it sent.
	truncated bool
	dirty     bool
	// sessionID is the session the CAPTURE read, not the one the flush reads. The
	// two differ by up to a whole tail interval, and a ClearContext inside that
	// window mints a new session. The browser keys its live entry by the span and
	// the session together, so a tail stamped with the replacement reaches an entry
	// that the call it describes never had.
	sessionID string
}

type generationProgressPublisher struct {
	mu       sync.Mutex
	counter  agent.ProgressCounter
	timer    *time.Timer
	closed   bool
	seen     bool
	revision uint64
	pending  *progressEmission
	// tailQueue holds the running-tool payloads flushTails produced, drained by the
	// SAME goroutine that publishes `pending`. Publishing them from the timer's own
	// goroutine put two publishers on one stream: a tail captured just before a
	// tool completed could reach the browser after the completion that dropped it,
	// which revived the live entry the reader had just seen finish -- the exact
	// case dropTailsLocked exists to prevent. The mutex orders the STATE; only one
	// publisher orders the delivery.
	tailQueue []map[string]interface{}
	tails     map[string]*runningToolTail
	tailTimer *time.Timer
	// tailInterval is a field rather than the constant below so a test can end the
	// window it waits on instead of sleeping through a real second.
	tailInterval time.Duration
	wake         chan struct{}
	publish      func(map[string]interface{})
	// sessionID identifies the provider session a tail belongs to. A span id is unique
	// inside one session and not across the sessions of one agent, so the browser
	// keys its live entry by the PAIR -- see the running_tool contract.
	sessionID func() string
}

func newGenerationProgressPublisher(publish func(map[string]interface{}), sessionID func() string) *generationProgressPublisher {
	publisher := &generationProgressPublisher{
		publish:      publish,
		sessionID:    sessionID,
		tailInterval: runningToolTailInterval,
		wake:         make(chan struct{}, 1),
	}
	go publisher.run()
	return publisher
}

func (p *generationProgressPublisher) report(update agent.ProgressUpdate) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if update.Operation == agent.ProgressOutputTail {
		p.noteTailLocked(update)
		p.mu.Unlock()
		return
	}
	// BEFORE the counter's own early return: a call that ended keeps no tail
	// whether or not the aggregate moved, and a completion for a scope the counter
	// never saw moves nothing. The browser drops its own entry when the result row
	// lands; this stops the worker re-broadcasting a tail for a finished span,
	// which would revive the entry the browser just dropped.
	p.dropTailsLocked(update)
	snapshot, changed := p.counter.Apply(update)
	if !changed {
		p.mu.Unlock()
		return
	}
	p.seen = true
	p.revision = generationProgressRevision.Add(1)
	urgent := update.Operation == agent.ProgressReset ||
		update.Operation == agent.ProgressModelReset ||
		update.Operation == agent.ProgressModelComplete ||
		update.Operation == agent.ProgressOutputComplete ||
		update.Operation == agent.ProgressOutputReset
	if urgent {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		p.queueLocked(snapshot)
		p.mu.Unlock()
		return
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(generationProgressInterval, p.flush)
	}
	p.mu.Unlock()
}

func (p *generationProgressPublisher) flush() {
	p.mu.Lock()
	if p.closed {
		p.timer = nil
		p.mu.Unlock()
		return
	}
	p.timer = nil
	p.queueLocked(p.counter.Snapshot())
	p.mu.Unlock()
}

func (p *generationProgressPublisher) queueLocked(snapshot agent.ProgressSnapshot) {
	p.pending = &progressEmission{info: progressInfo(snapshot, p.revision)}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *generationProgressPublisher) run() {
	for range p.wake {
		for {
			p.mu.Lock()
			emission := p.pending
			p.pending = nil
			tails := p.tailQueue
			p.tailQueue = nil
			closed := p.closed
			p.mu.Unlock()
			if emission == nil && len(tails) == 0 {
				if closed {
					return
				}
				break
			}
			// Tails first, then the counter: a tail describes a call that is still
			// running, and the counter snapshot may be the one that ends it.
			for _, payload := range tails {
				p.publish(payload)
			}
			if emission != nil {
				p.publish(emission.info)
			}
		}
	}
}

func (p *generationProgressPublisher) snapshotInfo() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.seen {
		return nil
	}
	return progressInfo(p.counter.Snapshot(), p.revision)
}

func (p *generationProgressPublisher) close() {
	p.mu.Lock()
	p.closed = true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if p.tailTimer != nil {
		p.tailTimer.Stop()
		p.tailTimer = nil
	}
	p.tails = nil
	p.counter.Apply(agent.ResetProgress())
	p.pending = nil
	p.tailQueue = nil
	select {
	case p.wake <- struct{}{}:
	default:
	}
	p.mu.Unlock()
}

func progressInfo(snapshot agent.ProgressSnapshot, revision uint64) map[string]interface{} {
	info := map[string]interface{}{
		contracts.SessionInfoKeyThinkingTokens:             snapshot.ThinkingTokens,
		contracts.SessionInfoKeyOutputBytes:                snapshot.OutputBytes,
		contracts.SessionInfoKeyGenerationProgressRevision: revision,
	}
	if snapshot.OutputBytes > 0 {
		info[contracts.SessionInfoKeyOutputBytesMinimum] = snapshot.OutputBytesMinimum
	}
	return info
}

// noteTailLocked records one running tool's latest output and arms the tail timer.
//
// It replaces rather than appends: a provider sends the tail it wants shown, and
// two providers disagree about whether their frames are deltas or totals. The one
// that sends deltas joins them itself, because only it knows where its own output
// ended.
func (p *generationProgressPublisher) noteTailLocked(update agent.ProgressUpdate) {
	if update.ScopeID == "" {
		return
	}
	text, clipped := clipOutputTail(update.Text)
	if p.tails == nil {
		p.tails = make(map[string]*runningToolTail)
	}
	sessionID := p.currentSessionID()
	entry := p.tails[update.ScopeID]
	// p.tails is keyed by the span id ALONE, and a span id is unique inside one
	// session rather than across the sessions of one agent. An entry that carries a
	// different session therefore describes a call that ended with that session, so
	// the new tail replaces it. Reusing it would keep the dead session's id, and the
	// dedup below would hold that id whenever the text repeats.
	if entry == nil || entry.sessionID != sessionID {
		entry = &runningToolTail{sessionID: sessionID}
		p.tails[update.ScopeID] = entry
	}
	truncated := update.Truncated || clipped
	if entry.text == text && entry.truncated == truncated {
		return
	}
	entry.text, entry.truncated, entry.dirty = text, truncated, true
	if p.tailTimer == nil {
		p.tailTimer = time.AfterFunc(p.tailInterval, p.flushTails)
	}
}

// dropTailsLocked forgets the tails of the calls one update ended.
//
// EXHAUSTIVE over the operation set, so an operation added later is a compile-time
// failure here rather than a tail the worker keeps re-broadcasting for a span that
// ended. The operations that keep a tail say so by name: each one reports PROGRESS
// inside a call that still runs, and a tail belongs to exactly such a call.
func (p *generationProgressPublisher) dropTailsLocked(update agent.ProgressUpdate) {
	switch update.Operation {
	case agent.ProgressReset:
		p.tails = nil
	case agent.ProgressOutputComplete, agent.ProgressOutputReset:
		delete(p.tails, update.ScopeID)
	case agent.ProgressModelText, agent.ProgressNativeTokens, agent.ProgressOutputDelta,
		agent.ProgressOutputTotal, agent.ProgressOutputTail,
		agent.ProgressModelComplete, agent.ProgressModelReset:
		// The call continues, so its tail stays. A model operation never ends a tool
		// call at all: the two counters are independent, and a turn that finished
		// thinking can still run the command it started.
	}
}

// dropTail forgets one span's tail, for the sink that stores the call's closing row.
//
// dropTailsLocked above reaches only a provider that reports ProgressOutputComplete.
// Copilot reports none, so its entries survived until the turn reset the whole map.
// The worker went on re-broadcasting the tail of a call whose result the browser
// already drew, which revives the live entry the reader watched finish. The closing
// row is the one event EVERY provider writes for a call that ended, so the drop sits
// there too and the next provider cannot forget it.
func (p *generationProgressPublisher) dropTail(spanID string) {
	if spanID == "" {
		return
	}
	p.mu.Lock()
	delete(p.tails, spanID)
	p.mu.Unlock()
}

// currentSessionID reads the provider session this publisher belongs to.
//
// The sessionID accessor is nil for a publisher whose owner states no session, so
// every caller goes through this one rather than repeating the check.
func (p *generationProgressPublisher) currentSessionID() string {
	if p.sessionID == nil {
		return ""
	}
	return p.sessionID()
}

// flushTails broadcasts one running-tool payload for each span whose tail moved.
//
// One payload per span rather than one carrying every span: `running_tool` states
// the span it describes, and the browser merges each into its own entry.
func (p *generationProgressPublisher) flushTails() {
	p.mu.Lock()
	p.tailTimer = nil
	if p.closed {
		p.mu.Unlock()
		return
	}
	payloads := make([]map[string]interface{}, 0, len(p.tails))
	for spanID, entry := range p.tails {
		if !entry.dirty {
			continue
		}
		entry.dirty = false
		payloads = append(payloads, map[string]interface{}{
			contracts.SessionInfoKeyRunningTool: map[string]interface{}{
				contracts.RunningToolFieldSpanId: spanID,
				// The session of the CAPTURE, which this flush can trail by a whole
				// interval. Reading the live session here stamped a tail from the
				// previous session with the id of the one ClearContext just minted.
				contracts.RunningToolFieldAgentSessionId:  entry.sessionID,
				contracts.RunningToolFieldOutputTail:      entry.text,
				contracts.RunningToolFieldOutputTruncated: entry.truncated,
			},
		})
	}
	p.tailQueue = append(p.tailQueue, payloads...)
	p.mu.Unlock()
	if len(payloads) > 0 {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// clipOutputTail keeps the LAST bytes of a tail, cut at a rune boundary.
//
// The last bytes are what a reader watches: a build prints its progress at the
// end. Cutting mid-rune would send a replacement character to the browser, so the
// cut advances to the next boundary.
func clipOutputTail(text string) (string, bool) {
	return agent.ClipTailBytes(text, runningToolTailBytes)
}
