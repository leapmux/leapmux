import type { ClassifiedEntry } from './chatEntryCache'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { createClassifiedEntryCache } from './chatEntryCache'
import { defaultHeightCtx } from './chatHeightEstimator'
import { createRowHeightInputs } from './chatRowHeightInputs'

/** A Codex reasoning row that classifies assistant_thinking once its span streams. */
function codexReasoning(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({ item: { type: 'reasoning', id: spanId, summary: [{ text: 'pondering the plan' }], content: [] }, threadId: 't', turnId: 'u' })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CODEX,
    spanId,
    spanType: 'reasoning',
  })
}

/** A factory wired to controllable prefs over a single thinking entry. */
function setup() {
  const [expandAgentThoughts, setExpand] = createSignal(true)
  const cache = createClassifiedEntryCache({
    messages: () => [codexReasoning('r1', 1n, 'span-1')],
    hasRenderableStreamBySpanId: () => true, // makes the reasoning row visible (assistant_thinking)
    hasNewerMessages: () => false,
    showHiddenMessages: () => false,
  })
  const entry = (): ClassifiedEntry => cache.visibleEntries()[0]
  const rhi = createRowHeightInputs({
    getEntry: id => (id === 'r1' ? entry() : undefined),
    getMessageUiBool: () => undefined, // no per-message override -> fall back to the global pref
    getLocalDiffView: () => undefined,
    expandAgentThoughts: () => expandAgentThoughts(),
    diffView: () => 'unified',
    workingDir: () => undefined,
    homeDir: () => undefined,
    heightCtx: () => defaultHeightCtx(800),
  })
  return { rhi, entry, setExpand }
}

describe('createrowheightinputs', () => {
  it('reads the prefs accessor FRESH on each buildRowInput call (not captured at construction)', () => {
    createRoot((dispose) => {
      const { rhi, entry, setExpand } = setup()
      // The thinking row defaults expanded to the global pref. buildRowInput must
      // re-read it per call -- a careless extraction that captured the pref at
      // construction would freeze this on the first value, leaving an off-screen
      // estimate stale after a global toggle.
      expect(rhi.buildRowInput(entry()).expanded).toBe(true)
      setExpand(false)
      expect(rhi.buildRowInput(entry()).expanded).toBe(false)
      dispose()
    })
  })

  it('estimates a positive height + breakdown from a virtual item\'s features thunk', () => {
    createRoot((dispose) => {
      const { rhi, entry } = setup()
      const item: VirtualItem = { id: 'r1', hasSpanLines: false, features: () => rhi.buildRowInput(entry()) }
      const breakdown = rhi.estimateItemBreakdown(item)
      expect(breakdown?.total).toBeGreaterThan(0)
      // The breakdown carries the contributing terms (the raw-JSON / WARN detail).
      expect(breakdown?.terms.length).toBeGreaterThan(0)
      dispose()
    })
  })

  it('returns null for a virtual item with no features thunk (caller seeds the default)', () => {
    createRoot((dispose) => {
      const { rhi } = setup()
      const item = { id: 'x', hasSpanLines: false } as VirtualItem
      expect(rhi.estimateItemBreakdown(item)).toBeNull()
      dispose()
    })
  })

  it('logHeightEstimateMiss is a no-op (no throw) for an unknown id', () => {
    createRoot((dispose) => {
      const { rhi } = setup()
      expect(() => rhi.logHeightEstimateMiss('missing', 100)).not.toThrow()
      dispose()
    })
  })
})
