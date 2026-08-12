package agent

import "sync"

// pendingPrompts holds a spawn's prompt until the child transcript that should
// carry it exists.
//
// Every provider that opens a subagent transcript needs this, because the spawn
// payload and the observation that CREATES the child transcript are different
// events: the prompt arrives first, and there is nowhere to persist it yet. So
// it is held under whatever key that provider links its child by (an ACP registry
// row key, a Codex child thread id, a Pi tool-call id) and spent once the
// transcript exists.
//
// One type rather than a map + mutex per provider, because the LIFETIME rules are
// the same for all of them and each hand-rolled copy had to re-derive them:
//
//   - First write wins. A provider can re-announce a spawn (a history replay, a
//     progress update carrying the same payload); the first prompt is the real
//     one, and a later one must not overwrite it.
//   - Take once. Spending removes the entry, so a replayed observation cannot
//     persist the prompt a second time.
//   - Forget on close, clear on session replace. An entry that is never spent --
//     a spawn whose child transcript never appears -- would otherwise retain a
//     whole prompt for the life of the process.
//
// The PARSING stays with each provider: only that provider knows which field of
// its own wire format carries the prompt. This type holds the mechanism, not the
// shape.
//
// The zero value is ready to use, so an embedder needs no constructor.
type pendingPrompts struct {
	mu    sync.Mutex
	byKey map[string]string
}

// remember stores prompt under key, keeping any prompt already there. A blank
// key or prompt is ignored: neither can be spent.
func (p *pendingPrompts) remember(key, prompt string) {
	if key == "" || prompt == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byKey == nil {
		p.byKey = make(map[string]string)
	}
	if _, ok := p.byKey[key]; !ok {
		p.byKey[key] = prompt
	}
}

// take returns the prompt held under key and removes it, or "" when none is
// held. Removing on read is what makes a replay idempotent.
func (p *pendingPrompts) take(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	prompt := p.byKey[key]
	delete(p.byKey, key)
	return prompt
}

// forget drops the prompt held under key without spending it, for a spawn whose
// child transcript will never appear.
func (p *pendingPrompts) forget(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byKey, key)
}

// peek returns the prompt held under key WITHOUT spending it, or "" when none
// is held. For assertions and diagnostics; the production paths take().
func (p *pendingPrompts) peek(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byKey[key]
}

// count returns how many prompts are held. For assertions and diagnostics: a
// non-zero count after a spawn's lifecycle ended is a retained prompt.
func (p *pendingPrompts) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byKey)
}

// clear drops every held prompt, for a replaced session. The next session can
// reuse a key, and the prompt behind it would open the wrong transcript.
func (p *pendingPrompts) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.byKey)
}
