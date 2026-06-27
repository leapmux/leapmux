import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { capInsertionOrder, createMessageUiState } from './chatMessageUiState'
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
      // A no-op set (same value) must NOT bump -- the height key shouldn't churn.
      ui.setMessageUiBool('m1', MESSAGE_UI_KEY.TOOL_RESULT_EXPANDED, true)
      expect(ui.getUiVersion('m1')).toBe(1)
      // A diff-view change bumps too, and a different key/override on the same id
      // keeps advancing the same per-id counter (any UI change invalidates measured height).
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
