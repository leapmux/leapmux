import type { CommandStreamSegment } from './chatTypes'
import { createStore } from 'solid-js/store'
import { getOrCreate } from '~/lib/getOrCreate'

// ---------------------------------------------------------------------------
// Command-stream slice
//
// Live per-span output/reasoning segments (keyed agentId -> spanId -> segments),
// appended as tool-execution deltas stream in. A self-contained sub-store; the
// only coupling to the windowing core is the message-version bump that wakes the
// auto-scroll effect, injected as `onMutate` so this slice owns no core state.
//
// Alongside the segment buffers it keeps a `renderable` set: the spans whose
// buffered stream holds RENDERABLE content -- at least one non-empty-text segment.
// The name is deliberately NOT "active"/"streaming": the bit does NOT mean "a
// producer is emitting right now." It is set on the first renderable delta and
// stays set until the buffer is cleared, so it means "this span has stream content
// worth rendering." It is its OWN reactive record, separate from the per-delta
// segment arrays, so a reader that only cares "does this span have a stream to
// show?" -- the classified-entry cache, which flips an empty-persisted reasoning
// row hidden<->visible on it -- can subscribe to `renderable` (which flips only
// TWICE per span: the first renderable delta, then the clear) and wake once per
// flip instead of re-running on every chunk.
//
// `renderable` is distinct from hasBufferedSegments (any buffered segment,
// INCLUDING a content-less reasoning_summary_break). The windowing trims spare on
// hasBufferedSegments so a prune can't discard a recorded part boundary, while
// VISIBILITY reads `renderable` so a break-only span doesn't flip an otherwise-
// empty row to a thinking bubble. renderable spans are a SUBSET of buffered ones.
// ---------------------------------------------------------------------------

// Map a command-stream delta method to its segment kind; unknown methods are
// plain output.
const METHOD_TO_SEGMENT_KIND: Record<string, CommandStreamSegment['kind']> = {
  'item/commandExecution/terminalInteraction': 'interaction',
  'item/reasoning/summaryTextDelta': 'reasoning_summary',
  'item/reasoning/textDelta': 'reasoning_content',
  'item/reasoning/summaryPartAdded': 'reasoning_summary_break',
}

export function createCommandStreamStore(deps: {
  /** Bump the agent's message version so the auto-scroll effect wakes on a stream change. */
  onMutate: (agentId: string) => void
}) {
  const [state, setState] = createStore<{
    byAgent: Record<string, Record<string, CommandStreamSegment[]>>
    // Spans whose buffered stream holds renderable content (agentId -> spanId ->
    // true). Maintained in lockstep with `byAgent` (set on the first renderable
    // delta, dropped on clear/prune), but tracked separately so a presence read
    // doesn't re-fire per delta.
    renderable: Record<string, Record<string, true>>
  }>({ byAgent: {}, renderable: {} })
  // Spans whose ONLY loaded row left the window (a mid-stream delete, a
  // beyond-window reseq, or an oldest-/newest-end trim) while their command stream
  // still held buffered segments. The structural-drop policy SPARES such a stream
  // (clearing it would lose the tool's in-flight segments -- or a recorded
  // reasoning_summary_break) instead of dropping it, and records it here. The
  // buffer is normally reclaimed when the stream ends (clear), with a turn-end
  // sweep (sweepOrphans) as the backstop for a stream that never completes. Tracked
  // per agent OUTSIDE the reactive store -- it's bookkeeping for a rare double-edge
  // (drop mid-stream), not rendered state.
  const orphanedBufferedSpans = new Map<string, Set<string>>()
  const recordOrphan = (agentId: string, spanId: string) => {
    getOrCreate(orphanedBufferedSpans, agentId, () => new Set<string>()).add(spanId)
  }
  // Drop a span's orphan record (it no longer needs the sweep backstop). Called
  // from dropSpan so a buffer drop -- a stream-end clear, a prune, or a sweep --
  // always reclaims the record in lockstep, and the set can't outlive the buffer.
  const forgetOrphan = (agentId: string, spanId: string) => {
    const set = orphanedBufferedSpans.get(agentId)
    if (set && set.delete(spanId) && set.size === 0)
      orphanedBufferedSpans.delete(agentId)
  }
  // Vivify the per-agent parent record before a nested path-set: the fine-grained
  // per-span setters below keep each span's reactivity isolated but can't navigate
  // into an undefined agent entry on a fresh agent's first delta. The mirror of
  // dropAgentIfEmpty, so the vivify-vs-collapse pair lives in one shape.
  const ensureAgentRecord = (key: 'byAgent' | 'renderable', agentId: string) => {
    if (!state[key][agentId])
      setState(key, agentId, {})
  }
  // Mark a span's stream renderable. Idempotent so only the first renderable delta
  // flips the reactive bit (a later same-span delta is a no-op for `renderable`, so
  // it doesn't re-wake the entry cache that subscribes to presence).
  const markRenderable = (agentId: string, spanId: string) => {
    if (state.renderable[agentId]?.[spanId])
      return
    ensureAgentRecord('renderable', agentId)
    setState('renderable', agentId, spanId, true)
  }
  // After dropping a span's per-agent entry, remove the now-empty parent record
  // so a long session doesn't accumulate one empty `{}` per agent that ever
  // streamed (`getByAgent`/`hasRenderableContent` already coalesce a missing parent
  // to empty, so this is invisible to readers).
  const dropAgentIfEmpty = (key: 'byAgent' | 'renderable', agentId: string) => {
    const parent = state[key][agentId]
    if (parent && Object.keys(parent).length === 0)
      setState(key, agentId, undefined!)
  }
  const dropRenderable = (agentId: string, spanId: string) => {
    if (state.renderable[agentId]?.[spanId]) {
      setState('renderable', agentId, spanId, undefined!)
      dropAgentIfEmpty('renderable', agentId)
    }
  }
  // Drop one span's buffer AND its renderable bit together -- the byAgent<->renderable
  // lockstep both `clear` and `pruneSpans` need. Returns whether a buffer was
  // actually removed, so the caller bumps the message version / collapses the
  // empty parent only then. The renderable bit is dropped even when the buffer is
  // already gone (dropRenderable no-ops if unset), so a presence bit left dangling by
  // a direct buffer clear can't linger out of lockstep.
  const dropSpan = (agentId: string, spanId: string): boolean => {
    const hadBuffer = !!state.byAgent[agentId] && spanId in state.byAgent[agentId]
    if (hadBuffer)
      setState('byAgent', agentId, spanId, undefined!)
    dropRenderable(agentId, spanId)
    // A buffer drop is the only way a span leaves the orphan set: forgetting here
    // keeps the record in lockstep with the buffer it tracks (a spared orphan that
    // later ends, gets pruned, or is swept can't linger as a stale record).
    forgetOrphan(agentId, spanId)
    return hadBuffer
  }
  // Whether `spanId` holds any buffered segment (a SUPERSET of renderable: catches a
  // content-less reasoning_summary_break too). Closure twin of hasBufferedSegments
  // so the orphan policy below can read it without `this`.
  const hasBuffered = (agentId: string, spanId: string): boolean => {
    const segments = state.byAgent[agentId]?.[spanId]
    return !!segments && segments.length > 0
  }
  // Drop one span's buffer (+ renderable bit + orphan record, via dropSpan),
  // collapsing the empty parent and waking subscribers only when a buffer actually
  // went away. Closure twin of the public `clear` so the orphan policy can reuse it.
  const clearSpan = (agentId: string, spanId: string) => {
    if (!spanId)
      return
    if (dropSpan(agentId, spanId)) {
      dropAgentIfEmpty('byAgent', agentId)
      deps.onMutate(agentId)
    }
  }
  // The shared spare-vs-reclaim policy both structural-drop paths apply: a span still
  // holding a buffered (mid-flight) stream is SPARED and recorded as orphaned (so the
  // turn-end / catch-up sweep reclaims a genuinely-stale buffer later, while a real
  // in-flight stream keeps its segments); an empty one is reclaimed now via `reclaim`.
  // The spare test is hasBuffered (a SUPERSET of renderable: a content-less
  // reasoning_summary_break counts), so a prune can't discard a recorded part boundary
  // and re-vivify from empty on the next delta. The batch prune and the single drop
  // differ ONLY in how they reclaim (collect into a list vs clear immediately), so
  // centralizing the decision keeps them from drifting on what's spared.
  const spareBufferedOrReclaim = (agentId: string, spanId: string, reclaim: (spanId: string) => void) => {
    if (hasBuffered(agentId, spanId))
      recordOrphan(agentId, spanId)
    else
      reclaim(spanId)
  }
  return {
    append(agentId: string, spanId: string, method: string, text: string) {
      if (!spanId)
        return
      const segmentKind: CommandStreamSegment['kind'] = METHOD_TO_SEGMENT_KIND[method] ?? 'output'
      if (!text && segmentKind !== 'reasoning_summary_break')
        return
      // Vivify the agent's span map first (see ensureAgentRecord): the per-span path
      // setter below keeps each span's reactivity fine-grained, but can't navigate
      // into an undefined agent entry on a fresh agent's first delta.
      ensureAgentRecord('byAgent', agentId)
      setState('byAgent', agentId, spanId, (prev = []) => {
        const last = prev.at(-1)
        if (segmentKind !== 'reasoning_summary_break' && last && last.kind === segmentKind) {
          return [
            ...prev.slice(0, -1),
            { kind: segmentKind, text: last.text + text },
          ]
        }
        return [...prev, { kind: segmentKind, text }]
      })
      // Mark the span renderable ONLY when a delta carries actual text. An empty
      // reasoning_summary_break is a content-less part boundary: it's buffered above
      // (so a multi-part summary renders with breaks) but must NOT flip an
      // otherwise-empty reasoning row from hidden to a thinking bubble -- only real
      // reasoning/output text does that. A later text delta marks it renderable then.
      if (text)
        markRenderable(agentId, spanId)
      deps.onMutate(agentId)
    },
    get(agentId: string, spanId: string): CommandStreamSegment[] {
      if (!spanId)
        return []
      return state.byAgent[agentId]?.[spanId] ?? []
    },
    /** Every span's segments for an agent ({} when none) -- the per-agent map. */
    getByAgent(agentId: string): Record<string, CommandStreamSegment[]> {
      return state.byAgent[agentId] ?? {}
    },
    /**
     * Whether `spanId`'s buffered stream holds renderable content right now -- i.e.
     * at least one non-empty-text segment has streamed and the buffer hasn't been
     * cleared. NOT "a producer is emitting right now": a span keeps this true after
     * its producer goes quiet, until its stream ends (clear) or the buffer is pruned.
     *
     * Reactive: reading it inside a tracked scope (the classified-entry cache)
     * subscribes to the span's presence bit, which flips only on the first renderable
     * delta and on clear -- so the cache re-classifies a row the moment its stream
     * first has content to show OR is cleared, without re-running per delta. The
     * windowing trims also call it (untracked) where they need the renderable signal.
     */
    hasRenderableContent(agentId: string, spanId: string): boolean {
      return !!state.renderable[agentId]?.[spanId]
    },
    /**
     * Whether `spanId` holds ANY buffered segment right now -- a SUPERSET of
     * hasRenderableContent: it additionally catches a span whose only buffered
     * content is a content-less reasoning_summary_break, which append() records but
     * deliberately does NOT mark renderable (a break must not flip an empty row to a
     * thinking bubble). The windowing survivor guards (trims + delete) spare a span
     * with a non-empty buffer, not just a renderable one, so a prune can't discard
     * recorded part boundaries and re-vivify the stream from empty on the next text
     * delta. A completed stream is cleared (buffer + renderable bit together), so a
     * non-empty buffer always means in-flight content worth keeping.
     */
    hasBufferedSegments(agentId: string, spanId: string): boolean {
      return hasBuffered(agentId, spanId)
    },
    clear(agentId: string, spanId: string) {
      // clearSpan keeps byAgent<->renderable<->orphan in lockstep even when the
      // buffer is already gone (a presence bit might still be set from a direct
      // clear); only an actual buffer removal collapses the empty parent and wakes
      // subscribers.
      clearSpan(agentId, spanId)
    },
    /**
     * Drop the command streams for `spanIds` whose messages just left the window
     * (an oldest-end trim or an explicit removal). Spans not present are skipped;
     * one mutation bump fires only if something was dropped. Without this, a long
     * tail-following session leaks a segment buffer per ever-streamed span even
     * after its message is trimmed away. Safe against the
     * stream-arrives-before-message race: callers pass only spanIds of messages
     * that WERE in the window, so a not-yet-persisted streaming span (whose
     * message isn't loaded) is never cleared. Callers that can drop the live tail
     * span (a newest-end trim) spare a still-buffered span first (see hasBufferedSegments).
     */
    pruneSpans(agentId: string, spanIds: Iterable<string>) {
      if (!state.byAgent[agentId])
        return
      let dropped = false
      for (const spanId of spanIds) {
        if (spanId && dropSpan(agentId, spanId))
          dropped = true
      }
      if (dropped) {
        dropAgentIfEmpty('byAgent', agentId)
        deps.onMutate(agentId)
      }
    },
    /**
     * Of `candidateSpanIds` (the dropped spans the SURVIVOR rule already cleared
     * for pruning -- no surviving row references them), return the ones safe to
     * prune now and SPARE + RECORD any still holding a buffered (mid-flight) stream.
     * The spare test is hasBufferedSegments, not hasRenderableContent: a span
     * buffering only a content-less reasoning_summary_break is spared too, so a
     * prune can't discard a recorded part boundary and re-vivify from empty on the
     * next delta. A spared span is recorded as orphaned so the turn-end / catch-up
     * sweep (sweepOrphans) reclaims a genuinely-stale buffer instead of leaking it,
     * while a real in-flight stream keeps its segments until it ends. The caller
     * prunes the returned list (directly or via the trim commit).
     */
    prunableSparingBuffered(agentId: string, candidateSpanIds: Iterable<string>): string[] {
      const prunable: string[] = []
      for (const spanId of candidateSpanIds)
        spareBufferedOrReclaim(agentId, spanId, sp => prunable.push(sp))
      return prunable
    },
    /**
     * Single-row analogue of prunableSparingBuffered for a dropped row's span (a
     * reseq-beyond-window drop or a delete). `stillReferenced` is whether a
     * SURVIVING row still carries the spanId (a tool_use opener and its tool_result
     * share one spanId, so dropping one member must not wipe the stream the other
     * renders). A referenced span is left intact; an unreferenced one is SPARED +
     * RECORDED if still buffered (mid-flight), else cleared immediately.
     */
    spareOrClearDroppedSpan(agentId: string, spanId: string | undefined, stillReferenced: boolean) {
      if (!spanId || stillReferenced)
        return
      spareBufferedOrReclaim(agentId, spanId, sp => clearSpan(agentId, sp))
    },
    /**
     * Reclaim command streams orphaned by a spared mid-stream drop that never
     * received their own stream-end. Called at a turn boundary (and on the
     * catch-up -> live transition), where any still-buffered orphan is genuinely
     * stuck. Touches ONLY spans EXPLICITLY recorded as orphaned: clears one iff
     * `isReferenced(spanId)` is false (no surviving row carries it). A still-
     * referenced span -- a spared live-tail span re-fetched on scroll-back -- is
     * left both buffered AND recorded, so a later drop of its last referencing row
     * is still reclaimable by the next sweep. The clear forgets the record (via
     * dropSpan), so the agent entry empties as its orphans are reclaimed.
     */
    sweepOrphans(agentId: string, isReferenced: (spanId: string) => boolean) {
      const set = orphanedBufferedSpans.get(agentId)
      if (!set)
        return
      for (const spanId of [...set]) {
        if (!isReferenced(spanId))
          clearSpan(agentId, spanId)
      }
    },
    /**
     * Drop ALL command-stream state for an agent at once -- its segment buffers,
     * renderable bits, and orphan records -- when the agent is closed. The
     * per-span prune/clear paths reclaim incrementally as rows leave the window;
     * this is the wholesale reclaim for the agent going away entirely, so none of
     * the three records (and especially the non-reactive orphanedBufferedSpans,
     * which only dropSpan ever trims) outlives the closed agent.
     */
    forgetAgent(agentId: string) {
      if (state.byAgent[agentId])
        setState('byAgent', agentId, undefined!)
      if (state.renderable[agentId])
        setState('renderable', agentId, undefined!)
      orphanedBufferedSpans.delete(agentId)
    },
  }
}
