import type { ToolProgressUpdate } from './chatToolProgress'
import type { MessageSpanIdentity } from '~/lib/messageSpan'
import { createRenderEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createToolProgressStore } from './chatToolProgress'

const RETRY = {
  attempt: 2,
  maxRetries: 5,
  retryDelayMs: 4000,
  errorStatus: 529,
  errorCategory: 'overloaded',
}

/** The provider session every span below belongs to, unless a test states another. */
const SESSION = 'sess-1'

function span(spanId: string, agentSessionId: string = SESSION): MessageSpanIdentity {
  return { spanId, agentSessionId }
}

/**
 * A store, and the count of the runs of a render effect that reads it through
 * `read`.
 *
 * A render effect runs once when it is created, and again at each store write
 * that `read` subscribes to. A count that stays at 1 therefore proves that the
 * calls since the start wrote nothing on that path.
 *
 * Make the calls OUTSIDE the root body, then call `dispose`. Inside the body,
 * Solid holds the re-run of an effect until the body returns, so the count
 * cannot see a write there.
 */
function storeWithReadCount(read: (store: ReturnType<typeof createToolProgressStore>) => unknown) {
  return createRoot((dispose) => {
    const store = createToolProgressStore()
    let runs = 0
    createRenderEffect(() => {
      read(store)
      runs++
    })
    return {
      store,
      runs: () => runs,
      dispose,
    }
  })
}

describe('createToolProgressStore', () => {
  it('records a heartbeat update and reads it back by span', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      expect(store.get('a1', span('toolu_A'))).toEqual({ elapsedSeconds: 30 })
      dispose()
    })
  })

  it('answers undefined for an unknown span or agent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      expect(store.get('a1', span('toolu_MISSING'))).toBeUndefined()
      expect(store.get('other', span('toolu_A'))).toBeUndefined()
      dispose()
    })
  })

  // A row with no span id has no badge, even when its agent holds entries.
  it('answers undefined for an empty span id', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      expect(store.get('a1', span(''))).toBeUndefined()
      expect(store.get('a1', span('', ''))).toBeUndefined()
      dispose()
    })
  })

  // An update with no span id identifies no row, so nothing could carry its
  // badge. The store must write nothing at all: even the first write for an
  // agent creates the agent record, and that wakes every badge that reads
  // through the record.
  it('ignores an update with no span id', () => {
    const { store, runs, dispose } = storeWithReadCount(store => store.get('a1', span('toolu_A')))
    try {
      store.apply('a1', {
        ...span(''),
        elapsedSeconds: 30,
        permissionCheck: true,
      })
      store.apply('a1', {
        ...span('', ''),
        retry: RETRY,
      })
      expect(runs(), 'the updates wrote nothing that a badge reads').toBe(1)
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      // A real update wakes the same reader, so the count above can see a write.
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      expect(runs()).toBeGreaterThan(1)
    }
    finally {
      dispose()
    }
  })

  // The property the span key exists for: one provider gives a span id that is
  // unique inside a session alone, so two sessions of one agent can use the
  // same span id. Each must keep its own entry.
  it('keeps two provider sessions of one agent apart', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A', 'sess-1'),
        elapsedSeconds: 30,
      })
      store.apply('a1', {
        ...span('toolu_A', 'sess-2'),
        elapsedSeconds: 90,
      })
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
      store.apply('a1', {
        ...span('toolu_A', ''),
        elapsedSeconds: 30,
      })
      expect(store.get('a1', span('toolu_A', ''))?.elapsedSeconds).toBe(30)
      expect(store.get('a1', span('toolu_A', 'sess-1'))).toBeUndefined()
      dispose()
    })
  })

  it('raises the elapsed time as heartbeats arrive', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 60,
      })
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(60)
      dispose()
    })
  })

  it('keeps parallel spans independent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.apply('a1', {
        ...span('toolu_B'),
        elapsedSeconds: 60,
      })
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
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.apply('a2', {
        ...span('toolu_A'),
        elapsedSeconds: 90,
      })
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
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 90,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        retry: RETRY,
      })
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
      store.apply('a1', {
        ...span('toolu_A'),
        retry: RETRY,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 120,
      })
      expect(store.get('a1', span('toolu_A'))?.retry).toEqual(RETRY)
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(120)
      dispose()
    })
  })

  // An explicit null is the agent's only "the retry resolved" signal.
  it('clears the retry on an explicit null, keeping every other field', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 90,
        retry: RETRY,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        retry: null,
      })
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
      store.apply('a1', {
        ...span('toolu_A', 'sess-1'),
        retry: RETRY,
      })
      store.apply('a1', {
        ...span('toolu_A', 'sess-2'),
        retry: RETRY,
      })
      store.apply('a1', {
        ...span('toolu_A', 'sess-1'),
        retry: null,
      })
      expect(store.get('a1', span('toolu_A', 'sess-1'))?.retry).toBeUndefined()
      expect(store.get('a1', span('toolu_A', 'sess-2'))?.retry).toEqual(RETRY)
      dispose()
    })
  })

  // The permission check uses the same three states as the retry: absent keeps
  // it, `true` sets it, and an explicit null ends it.
  it('sets the permission check on an explicit true', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        permissionCheck: true,
      })
      expect(store.get('a1', span('toolu_A'))?.permissionCheck).toBe(true)
      dispose()
    })
  })

  it('clears the permission check on an explicit null, keeping every other field', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 90,
        permissionCheck: true,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        permissionCheck: null,
      })
      // Asserted directly, not via toEqual: toEqual treats a key set to
      // undefined as absent, so it would pass even if the clear did nothing.
      expect(store.get('a1', span('toolu_A'))?.permissionCheck).toBeUndefined()
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(90)
      dispose()
    })
  })

  it('keeps the permission check when a later update omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        permissionCheck: true,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 120,
      })
      expect(store.get('a1', span('toolu_A'))?.permissionCheck).toBe(true)
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(120)
      dispose()
    })
  })

  // The worker sends the text it wants shown, joined on its side, so each new
  // tail REPLACES the old one. An append would repeat the shared prefix of two
  // overlapping windows, and an empty tail is a real value that erases the old
  // text for a tool that produced no output.
  it('replaces the output tail instead of appending it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        outputTail: 'first\n',
      })
      store.apply('a1', {
        ...span('toolu_A'),
        outputTail: 'first\nsecond\n',
      })
      expect(store.get('a1', span('toolu_A'))?.outputTail).toBe('first\nsecond\n')
      store.apply('a1', {
        ...span('toolu_A'),
        outputTail: '',
      })
      expect(store.get('a1', span('toolu_A'))?.outputTail).toBe('')
      dispose()
    })
  })

  // The truncation flag merges like every other field: an update that omits it
  // keeps the live badge's window note, and an explicit false ends it.
  it('keeps and clears the output truncation flag on later updates', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        outputTail: 'window',
        outputTruncated: true,
      })
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 60,
      })
      expect(store.get('a1', span('toolu_A'))?.outputTruncated).toBe(true)
      store.apply('a1', {
        ...span('toolu_A'),
        outputTail: 'full text',
        outputTruncated: false,
      })
      expect(store.get('a1', span('toolu_A'))?.outputTruncated).toBe(false)
      dispose()
    })
  })

  // The transport delivers at least once, so the same broadcast can arrive
  // twice. A second application must not accumulate any field: no appended
  // tail, no doubled retry count, no flipped flag.
  it('leaves the entry unchanged when the same update arrives twice', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      const update: ToolProgressUpdate = {
        ...span('toolu_A'),
        elapsedSeconds: 90,
        retry: RETRY,
        outputTail: 'building',
        outputTruncated: true,
        permissionCheck: true,
      }
      store.apply('a1', update)
      store.apply('a1', update)
      expect(store.get('a1', span('toolu_A'))).toEqual({
        elapsedSeconds: 90,
        retry: RETRY,
        outputTail: 'building',
        outputTruncated: true,
        permissionCheck: true,
      })
      dispose()
    })
  })

  it('drops one span and clears every span for an agent', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.apply('a1', {
        ...span('toolu_B'),
        elapsedSeconds: 30,
      })
      store.drop('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      expect(store.get('a1', span('toolu_B'))).toBeDefined()
      store.clearAgent('a1')
      expect(store.get('a1', span('toolu_B'))).toBeUndefined()
      dispose()
    })
  })

  // A drop or a clear on a store that holds nothing must not fail, and must
  // write nothing: a write would wake every badge that reads the agent record.
  it('tolerates a drop or clear on an empty store and writes nothing', () => {
    const { store, runs, dispose } = storeWithReadCount(store => store.get('a1', span('toolu_A')))
    try {
      expect(() => store.drop('a1', span('toolu_A'))).not.toThrow()
      expect(() => store.drop('a1', span(''))).not.toThrow()
      expect(() => store.clearAgent('a1')).not.toThrow()
      expect(runs(), 'the calls wrote nothing that a badge reads').toBe(1)
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      // A real update wakes the same reader, so the count above can see a write.
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      expect(runs()).toBeGreaterThan(1)
    }
    finally {
      dispose()
    }
  })

  // A drop or clear that identifies nothing must leave the live entries alone.
  // A drop that cleared the wrong record would blank a running tool's badge.
  it('leaves live entries alone when a drop or clear identifies something else', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.drop('a1', span('toolu_MISSING'))
      store.drop('a1', span(''))
      store.clearAgent('a2')
      expect(store.get('a1', span('toolu_A'))?.elapsedSeconds).toBe(30)
      dispose()
    })
  })

  // A heartbeat whose elapsed time the worker could not read omits the key, so
  // the entry keeps the last good value rather than blanking the badge.
  it('keeps the elapsed time when a later update omits it', () => {
    createRoot((dispose) => {
      const store = createToolProgressStore()
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 90,
      })
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
      store.apply('a1', {
        ...span('toolu_A'),
        elapsedSeconds: 30,
      })
      store.drop('a1', span('toolu_A'))
      expect(store.get('a1', span('toolu_A'))).toBeUndefined()
      // A clear over the emptied record is a no-op, and a later tool still lands.
      expect(() => store.clearAgent('a1')).not.toThrow()
      store.apply('a1', {
        ...span('toolu_B'),
        elapsedSeconds: 60,
      })
      expect(store.get('a1', span('toolu_B'))?.elapsedSeconds).toBe(60)
      store.clearAgent('a1')
      expect(store.get('a1', span('toolu_B'))).toBeUndefined()
      dispose()
    })
  })
})
