import type { ClassifiedEntry } from './chatEntryCache'
import type { RowUiStateDeps } from './chatMessageUiState'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { capInsertionOrder, createMessageUiState, resolveRowUiState } from './chatMessageUiState'
import { MESSAGE_UI_KEY } from './messageUiKeys'

describe('capInsertionOrder', () => {
  it('returns the map unchanged while within the cap', () => {
    const map = new Map<string, number>([['a', 1], ['b', 2]])
    expect(capInsertionOrder(map).size).toBe(2)
    expect(map.has('a')).toBe(true)
  })

  it('drops insertion-order-oldest entries once over the cap', () => {
    // The per-message UI-state maps deliberately outlive the windowed message list
    // (a trimmed row keeps its expand/diff choice when it scrolls back), so they
    // can't be pruned by message presence -- this cap is what keeps them bounded.
    const map = new Map<string, number>()
    for (let i = 0; i < 1100; i++)
      capInsertionOrder(map.set(`k${i}`, i))
    expect(map.size).toBe(1024) // MAX_UI_STATE_ENTRIES
    expect(map.has('k0')).toBe(false) // oldest 76 evicted (1100 - 1024)
    expect(map.has('k75')).toBe(false)
    expect(map.has('k76')).toBe(true) // oldest survivor
    expect(map.has('k1099')).toBe(true) // newest kept
  })

  it('never evicts a protected (currently-rendered) id, even when it is the oldest', () => {
    // 'k0' is the insertion-order-oldest entry but is currently rendered, so the
    // cap must skip it and evict the next-oldest UNprotected entries instead -- a
    // visible row's choice must not revert under the cap.
    const map = new Map<string, number>()
    const protect = new Set(['k0'])
    for (let i = 0; i < 1100; i++)
      capInsertionOrder(map.set(`k${i}`, i), protect)
    expect(map.size).toBe(1024)
    expect(map.has('k0')).toBe(true) // protected oldest survives
    expect(map.has('k1')).toBe(false) // the 76 evictions fall on k1..k76 instead
    expect(map.has('k76')).toBe(false)
    expect(map.has('k77')).toBe(true) // oldest survivor among the unprotected
    expect(map.has('k1099')).toBe(true)
  })
})

describe('createmessageuistate', () => {
  it('stores and reads a per-message diff-view override', () => {
    createRoot((dispose) => {
      const ui = createMessageUiState()
      expect(ui.getLocalDiffView('m1')).toBeUndefined()
      ui.setLocalDiffView('m1', 'split')
      expect(ui.getLocalDiffView('m1')).toBe('split')
      expect(ui.getLocalDiffView('m2')).toBeUndefined()
      dispose()
    })
  })

  it('stores and reads a per-message boolean flag, scoped by id and key', () => {
    createRoot((dispose) => {
      const ui = createMessageUiState()
      expect(ui.getMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBeUndefined()
      ui.setMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      expect(ui.getMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBe(true)
      // A different key on the same id is independent.
      expect(ui.getMessageUiBool('m1', MESSAGE_UI_KEY.THINKING)).toBeUndefined()
      // A different id is independent.
      expect(ui.getMessageUiBool('m2', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBeUndefined()
      dispose()
    })
  })

  it('retains state across many distinct ids (survives a window trim) up to the cap', () => {
    createRoot((dispose) => {
      const ui = createMessageUiState()
      ui.setLocalDiffView('old', 'split')
      // Toggle a flag on many other ids, as if scrolling through a long history.
      for (let i = 0; i < 500; i++)
        ui.setMessageUiBool(`m${i}`, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      // 'old' is far under the cap, so its choice survives -- a trimmed row keeps
      // its diff-view when it scrolls back into the window.
      expect(ui.getLocalDiffView('old')).toBe('split')
      dispose()
    })
  })

  it('never evicts a currently-rendered row even past the cap (protectedIds)', () => {
    createRoot((dispose) => {
      // 'visible' is mounted (in protectedIds) and is the FIRST toggled, so it is
      // the insertion-order-oldest -- exactly the entry a bare cap would evict once
      // a long session toggles past MAX_UI_STATE_ENTRIES distinct rows.
      const mounted = new Set<string>(['visible'])
      const ui = createMessageUiState({ protectedIds: () => mounted })
      ui.setMessageUiBool('visible', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      // Flood the cap with > MAX_UI_STATE_ENTRIES distinct off-screen toggles.
      for (let i = 0; i < 1100; i++)
        ui.setMessageUiBool(`off${i}`, MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      // The visible row's choice is protected from eviction; it would have aged out
      // of a bare insertion-order cap.
      expect(ui.getMessageUiBool('visible', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBe(true)
      // An off-screen row that fell off the front is gone (the cap still bounds memory).
      expect(ui.getMessageUiBool('off0', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED)).toBeUndefined()
      dispose()
    })
  })

  it('bumps getUiVersion on every real change but not on a no-op, scoped per id', () => {
    createRoot((dispose) => {
      const ui = createMessageUiState()
      expect(ui.getUiVersion('m1')).toBe(0) // untouched
      // A real bool toggle bumps the version.
      ui.setMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      expect(ui.getUiVersion('m1')).toBe(1)
      // A no-op set (same value) must NOT bump -- the estimate key shouldn't churn.
      ui.setMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      expect(ui.getUiVersion('m1')).toBe(1)
      // A diff-view change bumps too, and a different key/override on the same id
      // keeps advancing the same per-id counter (any UI change invalidates the estimate).
      ui.setLocalDiffView('m1', 'split')
      expect(ui.getUiVersion('m1')).toBe(2)
      ui.setLocalDiffView('m1', 'split') // no-op
      expect(ui.getUiVersion('m1')).toBe(2)
      // A different id has its own counter.
      expect(ui.getUiVersion('m2')).toBe(0)
      ui.setMessageUiBool('m2', MESSAGE_UI_KEY.THINKING, false)
      expect(ui.getUiVersion('m2')).toBe(1)
      expect(ui.getUiVersion('m1')).toBe(2) // unaffected
      dispose()
    })
  })

  it('refreshes insertion-order recency on a re-toggle so an actively-used row survives the cap', () => {
    createRoot((dispose) => {
      const ui = createMessageUiState() // no protected ids -> the cap is pure recency
      const KEY = MESSAGE_UI_KEY.THINKING
      // k0 is the oldest entry...
      ui.setMessageUiBool('k0', KEY, true)
      // ...then fill to one below the 1024 cap with fresh keys.
      for (let i = 1; i < 1023; i++)
        ui.setMessageUiBool(`k${i}`, KEY, true)
      // Re-toggle k0: delete-then-set moves it to the MRU end instead of leaving it
      // at the eviction front (a plain Map.set on an existing key keeps its slot).
      ui.setMessageUiBool('k0', KEY, false)
      // Two more fresh keys push over the cap, evicting the two now-oldest entries.
      ui.setMessageUiBool('k1023', KEY, true)
      ui.setMessageUiBool('k1024', KEY, true)
      // The re-touched k0 survives; the oldest UNtouched key (k1) is evicted instead.
      expect(ui.getMessageUiBool('k0', KEY)).toBe(false)
      expect(ui.getMessageUiBool('k1', KEY)).toBeUndefined()
      expect(ui.getMessageUiBool('k1024', KEY)).toBe(true)
      dispose()
    })
  })
})

describe('resolverowuistate', () => {
  const entry = (kind: string, opts: { id?: string, toolName?: string, provider?: AgentProvider } = {}): ClassifiedEntry =>
    ({
      msg: { id: opts.id ?? 'm1', agentProvider: opts.provider },
      category: opts.toolName ? { kind, toolName: opts.toolName } : { kind },
    } as unknown as ClassifiedEntry)

  const deps = (over: {
    bools?: Record<string, boolean>
    diffOverride?: 'unified' | 'split'
    expandAgentThoughts?: boolean
    diffView?: 'unified' | 'split'
  } = {}): RowUiStateDeps => ({
    getMessageUiBool: (id, key) => over.bools?.[`${id}|${key}`],
    getLocalDiffView: () => over.diffOverride,
    expandAgentThoughts: over.expandAgentThoughts ?? true,
    diffView: over.diffView ?? 'unified',
  })

  it('resolves a tool_result row to collapsed-by-default with no other flags', () => {
    expect(resolveRowUiState(entry('tool_result'), deps())).toEqual({
      collapsed: true, // TOOL_RESULT_EXPANDED default false -> collapsed
      expanded: false,
      toolBodyExpanded: false,
      diffView: 'unified',
    })
  })

  it('uncollapses a tool_result when its TOOL_RESULT_EXPANDED override is set', () => {
    const s = resolveRowUiState(entry('tool_result'), deps({ bools: { [`m1|${MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED}`]: true } }))
    expect(s.collapsed).toBe(false)
  })

  it('resolves a Codex commandExecution / ACP body tool_use collapse from TOOL_RESULT_EXPANDED', () => {
    // A settled commandExecution renders ToolResultMessage, and the ACP execute/read/
    // search/fetch bodies read getToolResultExpanded -- all keyed on TOOL_RESULT_EXPANDED.
    for (const tool of ['commandExecution', 'execute', 'read', 'search', 'fetch']) {
      const e = entry('tool_use', { toolName: tool })
      expect(resolveRowUiState(e, deps()).collapsed).toBe(true) // default collapsed
      expect(resolveRowUiState(e, deps({ bools: { [`m1|${MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED}`]: true } })).collapsed).toBe(false)
    }
  })

  it('resolves a Codex collab prompt collapse from its OWN CODEX_COLLAB_AGENT_TOOL_CALL key', () => {
    const collab = entry('tool_use', { toolName: 'collabAgentToolCall' })
    expect(resolveRowUiState(collab, deps()).collapsed).toBe(true) // default collapsed
    // Its own key uncollapses it...
    expect(resolveRowUiState(collab, deps({ bools: { [`m1|${MESSAGE_UI_KEY.CODEX_COLLAB_AGENT_TOOL_CALL}`]: true } })).collapsed).toBe(false)
    // ...and the shared tool_result key does NOT (the collab body reads its own key).
    expect(resolveRowUiState(collab, deps({ bools: { [`m1|${MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED}`]: true } })).collapsed).toBe(true)
  })

  it('leaves a webSearch tool_use uncollapsed-irrelevant (no result-body collapse key)', () => {
    // Settled webSearch is header-only (alwaysVisible), so it consumes no collapse flag.
    expect(resolveRowUiState(entry('tool_use', { toolName: 'webSearch' }), deps()).collapsed).toBe(false)
  })

  it('expands a thinking row per the global expandAgentThoughts pref', () => {
    expect(resolveRowUiState(entry('assistant_thinking'), deps({ expandAgentThoughts: true })).expanded).toBe(true)
    expect(resolveRowUiState(entry('assistant_thinking'), deps({ expandAgentThoughts: false })).expanded).toBe(false)
  })

  it('reads a codex thinking row under CODEX_REASONING, ignoring the shared THINKING key', () => {
    const codex = entry('assistant_thinking', { provider: AgentProvider.CODEX })
    // An override under CODEX_REASONING collapses it even though the pref expands.
    expect(resolveRowUiState(codex, deps({ expandAgentThoughts: true, bools: { [`m1|${MESSAGE_UI_KEY.CODEX_REASONING}`]: false } })).expanded).toBe(false)
    // An override under the SHARED THINKING key is NOT read for a codex row.
    expect(resolveRowUiState(codex, deps({ expandAgentThoughts: true, bools: { [`m1|${MESSAGE_UI_KEY.THINKING}`]: false } })).expanded).toBe(true)
  })

  it('expands a Bash tool_use body only when its TOOL_USE_LAYOUT override is set', () => {
    const bash = entry('tool_use', { toolName: 'Bash' })
    expect(resolveRowUiState(bash, deps()).toolBodyExpanded).toBe(false)
    expect(resolveRowUiState(bash, deps({ bools: { [`m1|${MESSAGE_UI_KEY.TOOL_USE_LAYOUT}`]: true } })).toolBodyExpanded).toBe(true)
  })

  it('reads an ACP execute body under OPENCODE_TOOL_CALL_UPDATE, NOT the Bash TOOL_USE_LAYOUT key', () => {
    const execute = entry('tool_use', { toolName: 'execute' })
    expect(resolveRowUiState(execute, deps()).toolBodyExpanded).toBe(false)
    // An override under the renderer's own key expands it.
    expect(resolveRowUiState(execute, deps({ bools: { [`m1|${MESSAGE_UI_KEY.OPENCODE_TOOL_CALL_UPDATE}`]: true } })).toolBodyExpanded).toBe(true)
    // An override under the Bash key is NOT read for an execute row.
    expect(resolveRowUiState(execute, deps({ bools: { [`m1|${MESSAGE_UI_KEY.TOOL_USE_LAYOUT}`]: true } })).toolBodyExpanded).toBe(false)
  })

  it('prefers a per-row diff-view override over the global pref, falling back when absent', () => {
    expect(resolveRowUiState(entry('tool_result'), deps({ diffView: 'unified', diffOverride: 'split' })).diffView).toBe('split')
    expect(resolveRowUiState(entry('tool_result'), deps({ diffView: 'split' })).diffView).toBe('split')
  })
})
