import { createRoot } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { createCommandStreamStore } from '~/stores/chatCommandStreams'

// The store now stores segments by resolved kind (the method->kind mapping moved to the Codex
// plugin's commandStreamSegmentKind hook), so these are the kinds the former methods mapped to.
const OUTPUT = 'output' as const
const SUMMARY = 'reasoning_summary' as const
const BREAK = 'reasoning_summary_break' as const

describe('chatCommandStreams', () => {
  it('merges consecutive same-kind deltas into one segment', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      store.append('a1', 's1', OUTPUT, 'foo')
      store.append('a1', 's1', OUTPUT, 'bar')
      expect(store.get('a1', 's1')).toEqual([{ kind: 'output', text: 'foobar' }])
      dispose()
    }))

  it('starts a new segment when the kind changes', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      store.append('a1', 's1', OUTPUT, 'cmd')
      store.append('a1', 's1', SUMMARY, 'thinking')
      expect(store.get('a1', 's1')).toEqual([
        { kind: 'output', text: 'cmd' },
        { kind: 'reasoning_summary', text: 'thinking' },
      ])
      dispose()
    }))

  it('never merges a reasoning_summary_break (always a fresh segment) and allows its empty text', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      store.append('a1', 's1', SUMMARY, 'part one')
      store.append('a1', 's1', BREAK, '') // empty text is allowed for a break
      store.append('a1', 's1', SUMMARY, 'part two')
      expect(store.get('a1', 's1')).toEqual([
        { kind: 'reasoning_summary', text: 'part one' },
        { kind: 'reasoning_summary_break', text: '' },
        { kind: 'reasoning_summary', text: 'part two' },
      ])
      dispose()
    }))

  it('records an empty reasoning_summary_break but does NOT mark the span renderable', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      // A content-less part boundary as the FIRST event: recorded (so a multi-part
      // summary renders with breaks), but the span stays NON-renderable so an
      // otherwise-empty reasoning row isn't flipped to a visible thinking bubble.
      store.append('a1', 's1', BREAK, '')
      expect(store.get('a1', 's1')).toEqual([{ kind: 'reasoning_summary_break', text: '' }])
      expect(store.hasRenderableContent('a1', 's1')).toBe(false)
      // A real text delta then marks it renderable.
      store.append('a1', 's1', SUMMARY, 'thinking...')
      expect(store.hasRenderableContent('a1', 's1')).toBe(true)
      dispose()
    }))

  it('hasBufferedSegments is a superset of hasRenderableContent: true for a break-only span', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      // Never streamed: no buffer, nothing renderable.
      expect(store.hasBufferedSegments('a1', 's1')).toBe(false)
      expect(store.hasRenderableContent('a1', 's1')).toBe(false)
      // A content-less break: recorded in the buffer but deliberately NOT renderable.
      // hasBufferedSegments must catch it so the windowing survivor guard spares it.
      store.append('a1', 's1', BREAK, '')
      expect(store.hasRenderableContent('a1', 's1')).toBe(false)
      expect(store.hasBufferedSegments('a1', 's1')).toBe(true)
      // A real text delta marks it renderable; the buffer is still non-empty.
      store.append('a1', 's1', SUMMARY, 'thinking')
      expect(store.hasRenderableContent('a1', 's1')).toBe(true)
      expect(store.hasBufferedSegments('a1', 's1')).toBe(true)
      // Clearing drops both the buffer and the renderable bit together.
      store.clear('a1', 's1')
      expect(store.hasBufferedSegments('a1', 's1')).toBe(false)
      expect(store.hasRenderableContent('a1', 's1')).toBe(false)
      // Blank spanId / unknown agent never report a buffer.
      expect(store.hasBufferedSegments('a1', '')).toBe(false)
      expect(store.hasBufferedSegments('nope', 's1')).toBe(false)
      dispose()
    }))

  it('ignores an empty non-break delta and a blank spanId without mutating', () =>
    createRoot((dispose) => {
      const onMutate = vi.fn()
      const store = createCommandStreamStore({ onMutate })
      store.append('a1', 's1', OUTPUT, '') // empty, non-break -> ignored
      store.append('a1', '', OUTPUT, 'x') // no spanId -> ignored
      expect(store.get('a1', 's1')).toEqual([])
      expect(onMutate).not.toHaveBeenCalled()
      dispose()
    }))

  it('bumps onMutate on a real append and a real clear, but not on a no-op clear', () =>
    createRoot((dispose) => {
      const onMutate = vi.fn()
      const store = createCommandStreamStore({ onMutate })
      store.append('a1', 's1', OUTPUT, 'x')
      expect(onMutate).toHaveBeenCalledTimes(1)
      store.clear('a1', 'absent') // span not present -> no-op
      expect(onMutate).toHaveBeenCalledTimes(1)
      store.clear('a1', 's1')
      expect(onMutate).toHaveBeenCalledTimes(2)
      expect(store.get('a1', 's1')).toEqual([])
      dispose()
    }))

  it('getByAgent returns every span map for an agent ({} when none)', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      expect(store.getByAgent('a1')).toEqual({})
      store.append('a1', 's1', OUTPUT, 'x')
      store.append('a1', 's2', OUTPUT, 'y')
      expect(Object.keys(store.getByAgent('a1')).sort()).toEqual(['s1', 's2'])
      dispose()
    }))

  it('tracks hasRenderableContent across the stream lifecycle (append -> clear)', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      expect(store.hasRenderableContent('a1', 's1')).toBe(false) // never streamed
      store.append('a1', 's1', OUTPUT, 'x')
      expect(store.hasRenderableContent('a1', 's1')).toBe(true) // first delta -> renderable
      store.append('a1', 's1', OUTPUT, 'y') // further delta -> still renderable
      expect(store.hasRenderableContent('a1', 's1')).toBe(true)
      store.clear('a1', 's1')
      expect(store.hasRenderableContent('a1', 's1')).toBe(false) // stream ended -> cleared
      expect(store.get('a1', 's1')).toEqual([])
      dispose()
    }))

  it('hasRenderableContent is false for a blank spanId and an unknown agent', () =>
    createRoot((dispose) => {
      const store = createCommandStreamStore({ onMutate: () => {} })
      expect(store.hasRenderableContent('a1', '')).toBe(false)
      expect(store.hasRenderableContent('nope', 's1')).toBe(false)
      dispose()
    }))

  it('pruneSpans drops both the buffer AND the renderable bit, leaving other spans intact', () =>
    createRoot((dispose) => {
      const onMutate = vi.fn()
      const store = createCommandStreamStore({ onMutate })
      store.append('a1', 's1', OUTPUT, 'x')
      store.append('a1', 's2', OUTPUT, 'y')
      onMutate.mockClear()

      store.pruneSpans('a1', ['s1', 'absent'])
      expect(onMutate).toHaveBeenCalledTimes(1) // one bump for the single real drop
      expect(store.hasRenderableContent('a1', 's1')).toBe(false)
      expect(store.get('a1', 's1')).toEqual([])
      // s2 untouched on both the buffer and the renderable bit.
      expect(store.hasRenderableContent('a1', 's2')).toBe(true)
      expect(store.get('a1', 's2')).toEqual([{ kind: 'output', text: 'y' }])
      dispose()
    }))

  describe('orphan-buffered-span policy', () => {
    it('prunableSparingBuffered returns empty-buffer spans to prune and spares + records buffered ones', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        store.append('a1', 'buffered', OUTPUT, 'mid-flight') // has a live buffer
        // 'empty' has no buffer; 'buffered' does. Only 'empty' is safe to prune.
        const prunable = store.prunableSparingBuffered('a1', ['empty', 'buffered'])
        expect(prunable).toEqual(['empty'])
        // The spared 'buffered' span keeps its segments...
        expect(store.get('a1', 'buffered')).toEqual([{ kind: 'output', text: 'mid-flight' }])
        // ...and is RECORDED, so a turn-end sweep (no surviving row references it)
        // reclaims it -- proving prunableSparingBuffered recorded it as orphaned.
        store.sweepOrphans('a1', () => false)
        expect(store.get('a1', 'buffered')).toEqual([])
        dispose()
      }))

    it('spareOrClearDroppedSpan: keeps a referenced span, spares a buffered one, clears an empty one', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        store.append('a1', 'ref', OUTPUT, 'x')
        store.append('a1', 'orphan', OUTPUT, 'y')

        // Referenced: left intact regardless of buffer.
        store.spareOrClearDroppedSpan('a1', 'ref', true)
        expect(store.get('a1', 'ref')).toEqual([{ kind: 'output', text: 'x' }])

        // Unreferenced + buffered: spared and recorded (not cleared now).
        store.spareOrClearDroppedSpan('a1', 'orphan', false)
        expect(store.get('a1', 'orphan')).toEqual([{ kind: 'output', text: 'y' }])
        // The sweep reclaims it once unreferenced, confirming it was recorded.
        store.sweepOrphans('a1', () => false)
        expect(store.get('a1', 'orphan')).toEqual([])

        // Unreferenced + empty buffer: cleared immediately (idempotent no-op here).
        store.spareOrClearDroppedSpan('a1', 'never-streamed', false)
        expect(store.get('a1', 'never-streamed')).toEqual([])
        dispose()
      }))

    it('sweepOrphans leaves a still-referenced orphan recorded for a later sweep', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        store.append('a1', 'orphan', OUTPUT, 'z')
        store.spareOrClearDroppedSpan('a1', 'orphan', false) // spared + recorded

        // Still referenced now: the sweep leaves it buffered AND recorded.
        store.sweepOrphans('a1', spanId => spanId === 'orphan')
        expect(store.get('a1', 'orphan')).toEqual([{ kind: 'output', text: 'z' }])
        // No longer referenced: the next sweep reclaims it (still recorded).
        store.sweepOrphans('a1', () => false)
        expect(store.get('a1', 'orphan')).toEqual([])
        dispose()
      }))

    it('clear forgets a span orphan record in lockstep, so a later sweep is a no-op', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        store.append('a1', 'orphan', OUTPUT, 'w')
        store.spareOrClearDroppedSpan('a1', 'orphan', false) // recorded as orphan
        // A normal stream-end clear: drops the buffer AND forgets the record.
        store.clear('a1', 'orphan')
        expect(store.get('a1', 'orphan')).toEqual([])
        // Re-buffer the SAME span id; the stale record must not have it swept away.
        store.append('a1', 'orphan', OUTPUT, 'reused')
        store.sweepOrphans('a1', () => false)
        expect(store.get('a1', 'orphan')).toEqual([{ kind: 'output', text: 'reused' }])
        dispose()
      }))
  })

  describe('forgetAgent', () => {
    it('drops all buffers, renderable bits, and orphan records for one agent, leaving others intact', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        store.append('a1', 's1', OUTPUT, 'visible') // renderable
        store.append('a1', 's2', BREAK, '') // buffered-but-not-renderable
        store.spareOrClearDroppedSpan('a1', 's3', false) // ... and one orphan record
        store.append('a1', 's3', OUTPUT, 'buffered') // give the orphan a buffer
        store.spareOrClearDroppedSpan('a1', 's3', false)
        store.append('a2', 's1', OUTPUT, 'other agent')

        store.forgetAgent('a1')

        expect(store.getByAgent('a1')).toEqual({})
        expect(store.hasRenderableContent('a1', 's1')).toBe(false)
        expect(store.hasBufferedSegments('a1', 's2')).toBe(false)
        // The orphan record is gone too: re-buffering s3 and sweeping with
        // nothing referenced must NOT reclaim it (a leaked record would).
        store.append('a1', 's3', OUTPUT, 'reused')
        store.sweepOrphans('a1', () => false)
        expect(store.get('a1', 's3')).toEqual([{ kind: 'output', text: 'reused' }])
        // The other agent is untouched.
        expect(store.get('a2', 's1')).toEqual([{ kind: 'output', text: 'other agent' }])
        dispose()
      }))

    it('is a no-op for an agent that never streamed', () =>
      createRoot((dispose) => {
        const store = createCommandStreamStore({ onMutate: () => {} })
        expect(() => store.forgetAgent('ghost')).not.toThrow()
        expect(store.getByAgent('ghost')).toEqual({})
        dispose()
      }))
  })
})
