import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { invalidateMessageParseCache } from '~/lib/messageParser'
import { createClassifiedEntryCache } from './chatEntryCache'

/** A Claude assistant text row (classifies visible). */
function assistantText(id: string, seq: bigint, text: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text }] } })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

/** An empty Codex reasoning row: hidden until its span streams (assistant_thinking). */
function emptyCodexReasoning(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({ item: { type: 'reasoning', id: spanId, summary: [], content: [] }, threadId: 't', turnId: 'u' })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CODEX,
    spanId,
    spanType: 'reasoning',
  })
}

/** A Claude tool_result row (classifies tool_result; sizes its diff from its opener). */
function claudeToolResult(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'user',
      message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'done' }] },
    })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

/** An optimistic local user row (seq 0n, classifies visible). */
function localText(id: string, text: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({ content: text })),
    contentCompression: ContentCompression.NONE,
    seq: 0n,
  })
}

describe('createclassifiedentrycache', () => {
  it('re-classifies a hidden reasoning row visible<->hidden as its command stream starts and clears', () => {
    createRoot((dispose) => {
      const [streaming, setStreaming] = createSignal(false)
      const messages = [assistantText('a1', 1n, 'hi'), emptyCodexReasoning('r1', 2n, 'span-1')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasRenderableStreamBySpanId: () => streaming(),
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      // No stream yet -> the empty reasoning row is hidden.
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1'])
      // The span starts streaming -> the row flips to visible (same seq). The
      // reactive presence read wakes the memo; the freshness check rebuilds it.
      setStreaming(true)
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1', 'r1'])
      // The stream is CLEARED with NO messages() change (streamEnd / completion):
      // the presence flip alone must re-classify the row back to hidden. Reading
      // presence reactively (not untracked) is exactly what makes THIS direction
      // work -- the bug was the row freezing visible after the stream ended.
      setStreaming(false)
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1'])
      dispose()
    })
  })

  it('rebuilds a span row\'s entry when its paired tool_use sibling becomes available', () => {
    createRoot((dispose) => {
      const [hasSibling, setHasSibling] = createSignal(false)
      // A span-bearing row (a tool_result reads its paired tool_use opener for its
      // diff height). When the opener arrives LATER (older-page prepend / reseq) the
      // entry must rebuild so the row re-estimates instead of staying frozen at its
      // no-sibling size.
      const messages = [emptyCodexReasoning('r1', 2n, 'span-1')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasToolUseSiblingBySpanId: () => hasSibling(),
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('r1')!
      expect(before.freshness.hasToolUseSibling).toBe(false)
      // The opener is indexed -> the freshness check rebuilds the entry (a new ref,
      // which busts the virtualizer's cached height via the changed estimateKey).
      setHasSibling(true)
      cache.visibleEntries()
      const after = cache.getEntry('r1')!
      expect(after.freshness.hasToolUseSibling).toBe(true)
      expect(after).not.toBe(before)
      dispose()
    })
  })

  it('rebuilds a tool_result entry when its paired opener\'s content version bumps', () => {
    createRoot((dispose) => {
      // A tool_result sizes its diff from the OPENER's input, and the opener is a
      // different message: an in-place same-seq opener edit bumps the OPENER's
      // content version while the result's own seq/id/contentVersion stay put, so
      // the entry (and its estimateKey) must rebuild off the opener version.
      const [openerVersion, setOpenerVersion] = createSignal(0)
      const messages = [claudeToolResult('tr1', 2n, 'span-1')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasToolUseSiblingBySpanId: () => true,
        toolUseSiblingContentVersionBySpanId: () => openerVersion(),
        hasNewerMessages: () => false,
        showHiddenMessages: () => true, // keep the result row visible regardless of classification
      })
      cache.visibleEntries()
      const before = cache.getEntry('tr1')!
      expect(before.category.kind).toBe('tool_result')
      expect(before.freshness.toolUseSiblingContentVersion).toBe(0)
      // The opener's body is replaced in place -> its version bumps -> the result
      // rebuilds even though nothing on the result's own id/seq moved.
      setOpenerVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('tr1')!
      expect(after.freshness.toolUseSiblingContentVersion).toBe(1)
      expect(after).not.toBe(before)
      dispose()
    })
  })

  it('does not consult the opener version for a non-tool_result row (no spurious rebuild)', () => {
    createRoot((dispose) => {
      // An assistant_text row never sizes from an opener, so its opener-version
      // probe must be skipped entirely -- a bump there must NOT rebuild it.
      let openerProbeReads = 0
      const [openerVersion, setOpenerVersion] = createSignal(0)
      const messages = [assistantText('a1', 1n, 'hi')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasToolUseSiblingBySpanId: () => true,
        toolUseSiblingContentVersionBySpanId: () => {
          openerProbeReads++
          return openerVersion()
        },
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('a1')!
      expect(before.freshness.toolUseSiblingContentVersion).toBe(0)
      const readsAfterFirst = openerProbeReads
      setOpenerVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('a1')!
      // Same reference: the assistant row never read the opener version, so the
      // bump didn't wake/rebuild it. The probe count also never advanced.
      expect(after).toBe(before)
      expect(openerProbeReads).toBe(readsAfterFirst)
      dispose()
    })
  })

  it('prunes departed-id entries when only hasVisibleEntries() is read (no leak)', () => {
    createRoot((dispose) => {
      // A leading HIDDEN row (cached because the emptiness scan must classify past
      // it to find a visible row) followed by a visible row.
      const [messages, setMessages] = createSignal<AgentChatMessage[]>([
        emptyCodexReasoning('r1', 1n, 'span-1'),
        assistantText('a1', 2n, 'hi'),
      ])
      const cache = createClassifiedEntryCache({
        messages,
        hasRenderableStreamBySpanId: () => false, // r1 stays hidden
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      // Read ONLY hasVisibleEntries() -- never visibleEntries(). It still caches
      // r1 (classified while scanning for the first visible row).
      expect(cache.hasVisibleEntries()).toBe(true)
      expect(cache.getEntry('r1')).toBeDefined()

      // r1 leaves the window. Reading ONLY hasVisibleEntries() again must prune it
      // -- the prune is no longer exclusive to visibleEntries(), so the cache can't
      // leak departed-id entries for a consumer that reads only this accessor.
      setMessages([assistantText('a1', 2n, 'hi')])
      expect(cache.hasVisibleEntries()).toBe(true)
      expect(cache.getEntry('r1')).toBeUndefined()
      dispose()
    })
  })

  it('prunes a departed cached entry even when the window size is unchanged (hasVisibleEntries only)', () => {
    createRoot((dispose) => {
      // Window 1: a HIDDEN leading row (cached while the emptiness scan classifies
      // past it) followed by a visible row.
      const [messages, setMessages] = createSignal<AgentChatMessage[]>([
        emptyCodexReasoning('r1', 1n, 'span-1'),
        assistantText('a1', 2n, 'hi'),
      ])
      const cache = createClassifiedEntryCache({
        messages,
        hasRenderableStreamBySpanId: () => false, // r1 stays hidden
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      expect(cache.hasVisibleEntries()).toBe(true)
      expect(cache.getEntry('r1')).toBeDefined()

      // Swap r1 OUT for a second VISIBLE row, keeping the window size at 2. The
      // emptiness scan short-circuits at the leading visible row a1, so it never
      // classifies (caches) a2 -- the cache stays {r1, a1}, size 2, equal to the
      // present-set size 2. A size-only prune guard (size > present.size) would
      // never fire and r1 would leak; the unconditional sweep drops it.
      setMessages([assistantText('a1', 2n, 'hi'), assistantText('a2', 3n, 'yo')])
      expect(cache.hasVisibleEntries()).toBe(true)
      expect(cache.getEntry('r1')).toBeUndefined() // pruned despite unchanged size
      dispose()
    })
  })

  it('hides trailing optimistic locals (seq 0n) while scrolled away from the tail', () => {
    createRoot((dispose) => {
      const [hasNewer, setHasNewer] = createSignal(false)
      const messages = [assistantText('a1', 1n, 'hi'), localText('local-1', 'pending')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasNewerMessages: hasNewer,
        showHiddenMessages: () => false,
      })
      // At the live tail the optimistic local renders.
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1', 'local-1'])
      // Windowed away: the trailing local is hidden (it reappears on jump-to-latest).
      setHasNewer(true)
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1'])
      dispose()
    })
  })

  it('prunes entries no longer in the window and reuses the cached ref for an unchanged row', () => {
    createRoot((dispose) => {
      const [messages, setMessages] = createSignal([assistantText('a1', 1n, 'hi'), assistantText('a2', 2n, 'yo')])
      const cache = createClassifiedEntryCache({
        messages,
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()
      expect(first.map(e => e.msg.id)).toEqual(['a1', 'a2'])
      const a1Entry = first[0]
      expect(cache.getEntry('a1')).toBe(a1Entry)

      // Drop a2 from the window; a1 is a new instance with the same id+seq.
      setMessages([assistantText('a1', 1n, 'hi')])
      const second = cache.visibleEntries()
      expect(second.map(e => e.msg.id)).toEqual(['a1'])
      expect(second[0]).toBe(a1Entry) // reused cached ref (no re-classification)
      expect(cache.getEntry('a2')).toBeUndefined() // pruned
      dispose()
    })
  })

  it('rebuilds an entry when its seq changes under a stable id (a reseq)', () => {
    createRoot((dispose) => {
      // A reseq (notification consolidation assigns MAX(seq)+1) keeps the id but moves
      // the seq -- the freshness signature's seq dimension must catch it and rebuild,
      // not hand back the pre-reseq classification.
      const [messages, setMessages] = createSignal<AgentChatMessage[]>([assistantText('a1', 1n, 'hi')])
      const cache = createClassifiedEntryCache({
        messages,
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()[0]
      expect(first.freshness.seq).toBe(1n)
      setMessages([assistantText('a1', 7n, 'hi')]) // same id, new seq
      const second = cache.visibleEntries()[0]
      expect(second).not.toBe(first) // rebuilt off the seq change
      expect(second.freshness.seq).toBe(7n)
      dispose()
    })
  })

  it('rebuilds an entry when its content version bumps (same-seq in-place body change)', () => {
    createRoot((dispose) => {
      // The store reuses the proxy on a same-seq in-place body replacement, so seq
      // and the object reference don't move -- only the content version does. The
      // cache must rebuild on that bump or it renders the pre-update body.
      const versions = new Map<string, number>()
      const msg = assistantText('a1', 1n, 'hi')
      const [messages, setMessages] = createSignal<AgentChatMessage[]>([msg])
      const cache = createClassifiedEntryCache({
        messages,
        contentVersionById: id => versions.get(id) ?? 0,
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()[0]
      const firstText = JSON.stringify(first.parsed.parentObject)

      // Re-trigger the memo WITHOUT a version bump (a new array carrying the same
      // proxy): the entry must be reused, proving the version -- not the array
      // change -- is what invalidates.
      setMessages([msg])
      expect(cache.visibleEntries()[0]).toBe(first)

      // Now simulate the store's same-seq in-place merge: replace the content on the
      // SAME object, then do exactly what updateExistingMessage does -- evict the
      // by-reference parse cache, bump the content version, and re-run the memo (in
      // production the messagesByAgent mutation that accompanies the bump does this).
      ;(msg as { content: Uint8Array }).content = new TextEncoder().encode(
        JSON.stringify({ type: 'assistant', message: { content: [{ type: 'text', text: 'CHANGED' }] } }),
      )
      invalidateMessageParseCache(msg)
      versions.set('a1', 1)
      setMessages([msg])

      const second = cache.visibleEntries()[0]
      expect(second).not.toBe(first) // rebuilt, not the stale cached ref
      expect(JSON.stringify(second.parsed.parentObject)).not.toBe(firstText) // reflects the new body
      dispose()
    })
  })

  it('shows hidden entries when showHiddenMessages is on', () => {
    createRoot((dispose) => {
      const [showHidden, setShowHidden] = createSignal(false)
      const messages = [assistantText('a1', 1n, 'hi'), emptyCodexReasoning('r1', 2n, 'span-1')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasNewerMessages: () => false,
        showHiddenMessages: showHidden,
      })
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1']) // r1 hidden
      setShowHidden(true)
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['a1', 'r1'])
      dispose()
    })
  })

  it('hasVisibleEntries reports presence without depending on visibleEntries()', () => {
    createRoot((dispose) => {
      const visible = createClassifiedEntryCache({
        messages: () => [assistantText('a1', 1n, 'hi')],
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      expect(visible.hasVisibleEntries()).toBe(true)

      const allHidden = createClassifiedEntryCache({
        messages: () => [emptyCodexReasoning('r1', 1n, 'span-1')],
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      expect(allHidden.hasVisibleEntries()).toBe(false)
      dispose()
    })
  })

  it('hasVisibleEntries agrees with visibleEntries() when only trailing locals remain while scrolled away', () => {
    createRoot((dispose) => {
      const [hasNewer, setHasNewer] = createSignal(false)
      const messages = [localText('local-1', 'pending')]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasNewerMessages: hasNewer,
        showHiddenMessages: () => false,
      })
      // At the live tail the optimistic local renders -> both accessors agree.
      expect(cache.visibleEntries().map(e => e.msg.id)).toEqual(['local-1'])
      expect(cache.hasVisibleEntries()).toBe(true)
      // Windowed away: the trailing local is hidden, so visibleEntries() is empty
      // and hasVisibleEntries() MUST agree (same hideTailLocals rule) -- otherwise
      // a consumer gating on it would render a non-empty container with zero rows.
      setHasNewer(true)
      expect(cache.visibleEntries()).toEqual([])
      expect(cache.hasVisibleEntries()).toBe(false)
      dispose()
    })
  })

  it('hasVisibleEntries hides trailing locals under showHiddenMessages too', () => {
    createRoot((dispose) => {
      const cache = createClassifiedEntryCache({
        messages: () => [localText('local-1', 'pending')],
        hasNewerMessages: () => true,
        showHiddenMessages: () => true,
      })
      // Only a trailing local while scrolled away: the tail-local hide applies even
      // under showHidden, so nothing renders and hasVisibleEntries agrees.
      expect(cache.visibleEntries()).toEqual([])
      expect(cache.hasVisibleEntries()).toBe(false)
      dispose()
    })
  })

  it('parses span_lines as [] for a well-formed but non-array payload, and as an array when valid', () => {
    createRoot((dispose) => {
      const withSpanLines = (id: string, seq: bigint, spanLines: string): AgentChatMessage => {
        const m = assistantText(id, seq, 'hi')
        m.spanLines = spanLines
        return m
      }
      // JSON.parse succeeds on each of these but only the last is an array. A
      // non-array (object / string / number) must NOT leak through as
      // parsedSpanLines -- downstream reads .length / iterates it as an array, and
      // a string value would otherwise iterate its characters as bogus columns.
      const messages = [
        withSpanLines('obj', 1n, '{"a":1}'),
        withSpanLines('str', 2n, '"hello"'),
        withSpanLines('num', 3n, '5'),
        withSpanLines('arr', 4n, '[null]'),
        // An ARRAY of primitives: passes Array.isArray, but a primitive element
        // would reach classFor (reads .type/.color off it) and render a bogus
        // colorless column -- the per-element filter drops non-object, non-null
        // elements, keeping only the valid object column.
        withSpanLines('prim', 5n, '[5, "x", null, {"type":"add"}]'),
        // Object-shaped junk: a nested array (`typeof [] === 'object'`) and a
        // type-less `{}` both pass a bare `typeof === 'object'` test but still
        // render as junk columns -- only an object carrying a string `type` (the
        // field classFor dispatches on) and the null sentinel survive.
        withSpanLines('junk', 6n, '[[1,2], {}, {"color":1}, null, {"type":"active","color":2,"span_id":"s"}]'),
        // `type` is PRESENT but not a string on each junk element: the column filter
        // dispatches on a STRING `type` (the field classFor reads), so a numeric, null,
        // or boolean `type` is dropped exactly like a missing one. Distinguishes the
        // `typeof type === 'string'` gate from a looser `'type' in el` / truthy check.
        withSpanLines('badtype', 7n, '[{"type":5}, {"type":null}, {"type":true}, {"type":"add"}]'),
      ]
      const cache = createClassifiedEntryCache({
        messages: () => messages,
        hasNewerMessages: () => false,
        showHiddenMessages: () => false,
      })
      // Materialize the entries so the cache classifies and parses every row.
      cache.visibleEntries()
      expect(cache.getEntry('obj')?.parsedSpanLines).toEqual([])
      expect(cache.getEntry('str')?.parsedSpanLines).toEqual([])
      expect(cache.getEntry('num')?.parsedSpanLines).toEqual([])
      expect(cache.getEntry('arr')?.parsedSpanLines).toEqual([null])
      expect(cache.getEntry('prim')?.parsedSpanLines).toEqual([null, { type: 'add' }])
      expect(cache.getEntry('junk')?.parsedSpanLines).toEqual([null, { type: 'active', color: 2, span_id: 's' }])
      expect(cache.getEntry('badtype')?.parsedSpanLines).toEqual([{ type: 'add' }])
      dispose()
    })
  })
})
