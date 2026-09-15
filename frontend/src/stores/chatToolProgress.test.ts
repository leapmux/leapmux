import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createToolProgressStore } from './chatToolProgress'

const RETRY = { attempt: 2, maxRetries: 5, retryDelayMs: 4000, errorStatus: 529, errorCategory: 'overloaded' }

/** The provider session every span below belongs to, unless a test states another. */
const SESSION = 'sess-1'

function span(spanId: string, agentSessionId: string = SESSION): MessageSpanIdentity {
  return { spanId, agentSessionId }
}

describe('createToolProgressStore', () => {
  it('records a heartbeat update and reads it back by span', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      expect(store.get('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  it('answers undefined for an unknown span, agent, or empty id', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      expect(store.get('a1', span('toolu_MISSING'))).toBeUndefined()
      expect(store.get('other', span('toolu_A'))).toBeUndefined()
      expect(store.get('a1', span(''))).toBeUndefined()
      dispose()
    })
  })

  // The property the span key exists for: one provider gives a span id that is
  // unique inside a session alone, so two sessions of one agent can name the
  // same span. Each must keep its own entry.
  it('keeps two provider sessions of one agent apart', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A', 'sess-1'), elapsedSeconds: 30 })
      store.apply('a1', { ...span('toolu_A', 'sess-2'), elapsedSeconds: 90 })
      expect(store.get('a1', span('toolu_A', 'sess-1'))?.elapsedSeconds).toBe(30)
      expect(store.get('a1', span('toolu_A', 'sess-2'))?.elapsedSeconds).toBe(90)
      store.drop('a1', span('toolu_A', 'sess-1'))
      expect(store.get('a1', span('toolu_A', 'sess-1'))).toBeUndefined()
      expect(store.get('a1', span('toolu_A', 'sess-2'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // A row that states no session and an update that states no session must still
  // meet, because messageSpanKey reads an absent session as ''.
  it('reads a session-less update back through a session-less row', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A', ''), elapsedSeconds: 30 })
      expect(store.get('a1', span('toolu_A', ''))?.elapsedSeconds).toBe(30)
      expect(store.get('a1', span('toolu_A', 'sess-1'))).toBeUndefined()
      dispose()
    })
  })

  it('ignores an update with no span id -- nothing could carry its badge', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span(''), elapsedSeconds: 30 })
      expect(store.get('a1', span(''))).toBeUndefined()
      dispose()
    })
  })

  it('raises the elapsed time as heartbeats arrive', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 60 })
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(60)
      dispose()
    })
  })

  it('keeps parallel spans independent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      store.apply('a1', { ...span('toolu_B'), elapsedSeconds: 60 })
      expect(store.get('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      expect(store.get('a1', span('toolu_B'))).toEqual({ elapsedSeconds: 60 })
      store.drop('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_B'))?.elapsedSeconds).toBe(60)
      dispose()
    })
  })

  it('keeps two agents apart', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      store.apply('a2', { ...span('toolu_A'), elapsedSeconds: 90 })
      store.clearAgent('a1')
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      expect(store.get('a2', span('toolu_A'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // The merge rule this store exists for: a retry frame reports elapsed 0, so a
  // REPLACE would rewind the clock the heartbeats maintain twice a minute.
  it('merges a retry update without disturbing the elapsed time', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 90 })
      store.apply('a1', { ...span('toolu_A'), retry: RETRY })
      expect(store.get('a1', span('toolu_A'))).toEqual({
        elapsedSeconds: 90,
        retry: RETRY,
      })
      dispose()
    })
  })

  it('keeps the retry when a later heartbeat omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), retry: RETRY })
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 120 })
      expect(store.get('a1', span('toolu_A'))?.retry).toEqual(RETRY)
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(120)
      dispose()
    })
  })

  // An explicit null is the agent's only "the retry resolved" signal.
  it('clears the retry on an explicit null, keeping every other field', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 90, retry: RETRY })
      store.apply('a1', { ...span('toolu_A'), retry: null })
      // Asserted directly, not via toEqual: toEqual treats a key set to
      // undefined as absent, so it would pass even if the clear did nothing but
      // leave the old object in place.
      expect(store.get('a1', span('toolu_A'))?.retry).toBeUndefined()
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // The retry clear takes its own path set, so it must key by the same pair the
  // merge above does. A clear that keyed by the span alone would reach the other
  // session's entry.
  it('clears the retry of one session alone', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A', 'sess-1'), retry: RETRY })
      store.apply('a1', { ...span('toolu_A', 'sess-2'), retry: RETRY })
      store.apply('a1', { ...span('toolu_A', 'sess-1'), retry: null })
      expect(store.get('a1', span('toolu_A', 'sess-1'))?.retry).toBeUndefined()
      expect(store.get('a1', span('toolu_A', 'sess-2'))?.retry).toEqual(RETRY)
      dispose()
    })
  })

  it('drops one span and clears every span for an agent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      store.apply('a1', { ...span('toolu_B'), elapsedSeconds: 30 })
      store.drop('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      expect(store.get('a1', span('toolu_B'))).toBeDefined()
      store.clearAgent('a1')
      expect(store.get('a1', span('toolu_B'))).toBeUndefined()
      dispose()
    })
  })

  it('tolerates a drop or clear for something it never held', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      expect(() => store.drop('a1', span('toolu_A'))).not.toThrow()
      expect(() => store.drop('a1', span(''))).not.toThrow()
      expect(() => store.clearAgent('a1')).not.toThrow()
      dispose()
    })
  })

  // A heartbeat whose elapsed time the worker could not read omits the key, so
  // the entry keeps the last good value rather than blanking the badge.
  it('keeps the elapsed time when a later update omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 90 })
      store.apply('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  // The parent record is deliberately NOT collapsed when its last span drops:
  // every mounted badge subscribes through it, so a delete-then-recreate wakes
  // all of them for nothing. clearAgent must therefore tolerate an empty record
  // AND must not write to one.
  it('leaves an emptied agent record in place, and clears it only when it holds a span', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', { ...span('toolu_A'), elapsedSeconds: 30 })
      store.drop('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      // A clear over the emptied record is a no-op, and a later tool still lands.
      expect(() => store.clearAgent('a1')).not.toThrow()
      store.apply('a1', { ...span('toolu_B'), elapsedSeconds: 60 })
      expect(store.get('a1', span('toolu_B'))?.elapsedSeconds).toBe(60)
      store.clearAgent('a1')
      expect(store.get('a1', span('toolu_B'))).toBeUndefined()
      dispose()
    })
  })
})
