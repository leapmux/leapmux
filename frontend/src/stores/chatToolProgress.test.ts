import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createToolProgressStore } from './chatToolProgress'

const RETRY = { attempt: 2, maxRetries: 5, retryDelayMs: 4000, errorStatus: 529, errorCategory: 'overloaded' }

describe('createToolProgressStore', () => {
  it('records a heartbeat update and reads it back by span', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      expect(store.get('a1', 'toolu_A')).toEqual({ toolName: 'Bash', elapsedSeconds: 30 })
      dispose()
    })
  })

  it('answers undefined for an unknown span, agent, or empty id', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      expect(store.get('a1', 'toolu_MISSING')).toBeUndefined()
      expect(store.get('other', 'toolu_A')).toBeUndefined()
      expect(store.get('a1', '')).toBeUndefined()
      dispose()
    })
  })

  it('ignores an update with no span id -- nothing could carry its badge', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: '', toolName: 'Bash', elapsedSeconds: 30 })
      expect(store.get('a1', '')).toBeUndefined()
      dispose()
    })
  })

  it('raises the elapsed time as heartbeats arrive', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 60 })
      expect(store.get('a1', 'toolu_A')?.elapsedSeconds).toBe(60)
      dispose()
    })
  })

  it('keeps parallel spans independent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      store.apply('a1', { spanId: 'toolu_B', toolName: 'Read', elapsedSeconds: 60 })
      expect(store.get('a1', 'toolu_A')).toEqual({ toolName: 'Bash', elapsedSeconds: 30 })
      expect(store.get('a1', 'toolu_B')).toEqual({ toolName: 'Read', elapsedSeconds: 60 })
      store.drop('a1', 'toolu_A')
      expect(store.get('a1', 'toolu_B')?.elapsedSeconds).toBe(60)
      dispose()
    })
  })

  it('keeps two agents apart', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      store.apply('a2', { spanId: 'toolu_A', toolName: 'Read', elapsedSeconds: 90 })
      store.clearAgent('a1')
      expect(store.get('a1', 'toolu_A')).toBeUndefined()
      expect(store.get('a2', 'toolu_A')?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // The merge rule this store exists for: a retry frame reports elapsed 0, so a
  // REPLACE would rewind the clock the heartbeats maintain twice a minute.
  it('merges a retry update without disturbing the elapsed time', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', elapsedSeconds: 90 })
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', subagentType: 'Explore', retry: RETRY })
      expect(store.get('a1', 'toolu_A')).toEqual({
        toolName: 'Agent',
        elapsedSeconds: 90,
        subagentType: 'Explore',
        retry: RETRY,
      })
      dispose()
    })
  })

  it('keeps the retry when a later heartbeat omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', retry: RETRY })
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', elapsedSeconds: 120 })
      expect(store.get('a1', 'toolu_A')?.retry).toEqual(RETRY)
      expect(store.get('a1', 'toolu_A')?.elapsedSeconds).toBe(120)
      dispose()
    })
  })

  // An explicit null is the agent's only "the retry resolved" signal.
  it('clears the retry on an explicit null, keeping every other field', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', elapsedSeconds: 90, subagentType: 'Explore', retry: RETRY })
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Agent', retry: null })
      // Asserted directly, not via toEqual: toEqual treats a key set to
      // undefined as absent, so it would pass even if the clear did nothing but
      // leave the old object in place.
      expect(store.get('a1', 'toolu_A')?.retry).toBeUndefined()
      expect(store.get('a1', 'toolu_A')?.elapsedSeconds).toBe(90)
      expect(store.get('a1', 'toolu_A')?.subagentType).toBe('Explore')
      dispose()
    })
  })

  it('drops one span and clears every span for an agent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      store.apply('a1', { spanId: 'toolu_B', toolName: 'Read', elapsedSeconds: 30 })
      store.drop('a1', 'toolu_A')
      expect(store.get('a1', 'toolu_A')).toBeUndefined()
      expect(store.get('a1', 'toolu_B')).toBeDefined()
      store.clearAgent('a1')
      expect(store.get('a1', 'toolu_B')).toBeUndefined()
      dispose()
    })
  })

  it('tolerates a drop or clear for something it never held', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      expect(() => store.drop('a1', 'toolu_A')).not.toThrow()
      expect(() => store.drop('a1', '')).not.toThrow()
      expect(() => store.clearAgent('a1')).not.toThrow()
      dispose()
    })
  })

  // A tool that reports before its name is known must not read as `undefined` on
  // the card; the entry keeps whatever name it already had.
  it('keeps the known tool name when an update omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { spanId: 'toolu_A', toolName: 'Bash', elapsedSeconds: 30 })
      store.apply('a1', { spanId: 'toolu_A', elapsedSeconds: 60 })
      expect(store.get('a1', 'toolu_A')?.toolName).toBe('Bash')
      dispose()
    })
  })
})
