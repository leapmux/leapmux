import type { ClassifiedEntryCacheDeps } from './chatEntryCache'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { ZCODE_EVENT, ZCODE_TOOL, ZCODE_TOOL_KIND } from '~/generated/contracts/zcode-protocol'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { invalidateMessageParseCache } from '~/lib/messageParser'
import { createClassifiedEntryCache, heightKeyForEntry, renderKeyForEntry } from './chatEntryCache'
import { resolvedSpanRole } from './providers/registry'
import { prepareMessage } from './rowPreparation'

/**
 * A ZCode `scheduled` ExitPlanMode call whose own frame carries no arguments: the
 * plan arrives through the SUPPLEMENTAL stream, so the raw bytes and the resolved
 * payload are two different rows -- `tool_use` before the supplement lands,
 * `assistant_plan` after it. The freshest case a cache dimension can name: the
 * category itself moves, not just the body.
 */
function zcodeScheduledExitPlanMode(supplementalPlan?: string): AgentChatMessage {
  const encode = (value: unknown) => new TextEncoder().encode(JSON.stringify(value))
  return create(AgentChatMessageSchema, {
    id: 'z1',
    source: MessageSource.AGENT,
    content: encode({
      type: ZCODE_EVENT.ToolUpdated,
      payload: { kind: ZCODE_TOOL_KIND.Scheduled, toolCallId: 'call-1', toolName: ZCODE_TOOL.ExitPlanMode, input: {} },
      sessionId: 's-1',
      seq: 1,
    }),
    contentCompression: ContentCompression.NONE,
    supplementalContentCompression: ContentCompression.NONE,
    // Omitted (not undefined) while no supplement has landed; `create` rejects an explicit undefined.
    ...(supplementalPlan === undefined
      ? {}
      : {
          supplementalContent: encode({
            provider: {
              type: ZCODE_EVENT.ToolUpdated,
              payload: { kind: ZCODE_TOOL_KIND.Scheduled, toolCallId: 'call-1', input: { plan: supplementalPlan } },
            },
          }),
        }),
    supplementalRevision: supplementalPlan === undefined ? 0n : 1n,
    seq: 4n,
    agentProvider: AgentProvider.ZCODE,
    spanId: 'call-1',
  })
}

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

/** A Claude tool_result row (classifies tool_result; sizes its diff from its request). */
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

/** A Claude tool_use row that can render hidden paired tool_result data. */
function claudeToolUse(id: string, seq: bigint, spanId: string, toolName = 'TaskGet'): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'tool_use', id: 'toolu_1', name: toolName, input: { task_id: 'task-1' } }],
      },
    })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

/** A completed ACP update, whose category is tool_use but whose span role is result. */
function acpToolResult(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify({
      sessionUpdate: 'tool_call_update',
      toolCallId: spanId,
      status: 'completed',
      rawOutput: 'No file changes occurred.',
    })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.OPENCODE,
    spanId,
    spanType: 'edit',
  })
}

/**
 * A Claude user row forwarded from a subagent. It carries `parent_tool_use_id`,
 * so it classifies as the prompt SENT to the subagent in the parent's transcript
 * and as an ordinary user message inside the child's own.
 */
function forwardedUserText(id: string, seq: bigint, parentToolUseId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'user',
      parent_tool_use_id: parentToolUseId,
      message: { role: 'user', content: [{ type: 'text', text: 'keep going' }] },
    })),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

function createTestClassifiedEntryCache(
  deps: Omit<ClassifiedEntryCacheDeps, 'role'> & Partial<Pick<ClassifiedEntryCacheDeps, 'role'>>,
) {
  return createClassifiedEntryCache({
    ...deps,
    role: deps.role ?? (message => resolvedSpanRole(prepareMessage(message).resolved, message.agentProvider)),
  })
}

describe('createClassifiedEntryCache', () => {
  it('uses the resolver-selected message and revision as the row authority', () => {
    createRoot((dispose) => {
      const stale = zcodeScheduledExitPlanMode()
      const selected = zcodeScheduledExitPlanMode('Use the selected supplement')
      const selectedPrepared = prepareMessage(selected)
      const cache = createTestClassifiedEntryCache({
        messages: () => [stale],
        resolvedMessage: () => ({
          message: selected,
          original: selectedPrepared.original,
          resolved: selectedPrepared.resolved,
          revision: { id: selected.id, seq: selected.seq, contentVersion: 7, supplementalRevision: selected.supplementalRevision },
        }),
        role: () => 'request',
        showHiddenMessages: () => false,
      })
      const entry = cache.visibleEntries()[0]!
      expect(entry.message).toBe(selected)
      expect(entry.category.kind).toBe('assistant_plan')
      expect(entry.freshness.revisionKey).toContain('|7|1')
      dispose()
    })
  })
  it('rebuilds a span row\'s entry when its paired tool_use sibling becomes available', () => {
    createRoot((dispose) => {
      const [hasSibling, setHasSibling] = createSignal(false)
      // A tool_result reads its paired tool_use request for its rendered shape.
      // When the request arrives LATER (older-page prepend / reseq) the entry must
      // rebuild so the row's measured-height key changes instead of staying frozen
      // at its no-sibling shape.
      const messages = [claudeToolResult('r1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => hasSibling() ? { id: 'request', seq: 1n, contentVersion: 0, supplementalRevision: 0n } : undefined,
        showHiddenMessages: () => true,
      })
      cache.visibleEntries()
      const before = cache.getEntry('r1')!
      expect(before.category.kind).toBe('tool_result')
      expect(before.freshness.revisionKey).not.toContain('~request=')
      // The request is indexed -> the freshness check rebuilds the entry (a new ref,
      // which busts the virtualizer's cached DOM height via the changed heightKey).
      setHasSibling(true)
      cache.visibleEntries()
      const after = cache.getEntry('r1')!
      expect(after.freshness.revisionKey).toContain('~request=')
      expect(after).not.toBe(before)
      dispose()
    })
  })

  it('tracks the request for a result role that uses the tool_use category', () => {
    createRoot((dispose) => {
      const [hasRequest, setHasRequest] = createSignal(false)
      const messages = [acpToolResult('result', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => hasRequest() ? { id: 'request', seq: 1n, contentVersion: 0, supplementalRevision: 0n } : undefined,
        showHiddenMessages: () => false,
      })

      cache.visibleEntries()
      const before = cache.getEntry('result')!
      expect(before.category.kind).toBe('tool_use')
      expect(before.freshness.revisionKey).not.toContain('~request=')
      setHasRequest(true)
      cache.visibleEntries()
      const after = cache.getEntry('result')!
      expect(after).not.toBe(before)
      expect(after.freshness.revisionKey).toContain('~request=')
      dispose()
    })
  })

  it('rebuilds a tool_result entry when its paired request\'s content version bumps', () => {
    createRoot((dispose) => {
      // A tool_result sizes its diff from the REQUEST's input, and the request is a
      // different message: an in-place same-seq request edit bumps the REQUEST's
      // content version while the result's own seq/id/contentVersion stay put, so
      // the entry (and its heightKey) must rebuild off the request version.
      const [requestVersion, setRequestVersion] = createSignal(0)
      const messages = [claudeToolResult('tr1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => ({ id: 'request', seq: 1n, contentVersion: requestVersion(), supplementalRevision: 0n }),
        showHiddenMessages: () => true, // keep the result row visible regardless of classification
      })
      cache.visibleEntries()
      const before = cache.getEntry('tr1')!
      expect(before.category.kind).toBe('tool_result')
      expect(before.freshness.revisionKey).toMatch(/~request=\d+:request\|1\|0\|0$/)
      // The request's body is replaced in place -> its version bumps -> the result
      // rebuilds even though nothing on the result's own id/seq moved.
      setRequestVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('tr1')!
      expect(after.freshness.revisionKey).toMatch(/~request=\d+:request\|1\|1\|0$/)
      expect(after).not.toBe(before)
      dispose()
    })
  })

  it('rebuilds a tool_result entry when its paired request identity changes at the same content version', () => {
    createRoot((dispose) => {
      const [requestRevision, setRequestRevision] = createSignal({ id: 'request-a', seq: 1n, contentVersion: 0, supplementalRevision: 0n })
      const messages = [claudeToolResult('tr1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => requestRevision(),
        showHiddenMessages: () => true,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tr1')!
      const beforeHeightKey = heightKeyForEntry(before, 0)

      setRequestRevision({ id: 'request-b', seq: 3n, contentVersion: 0, supplementalRevision: 0n })
      cache.visibleEntries()
      const after = cache.getEntry('tr1')!

      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  it('rebuilds a tool_use entry when its paired hidden result content version bumps', () => {
    createRoot((dispose) => {
      // Claude Task* tool_use rows render details from their hidden tool_result
      // sibling. A same-seq result edit bumps the RESULT version while the request's
      // seq/id/contentVersion stay put, so the request entry and height key must
      // rebuild off the result version.
      const [resultVersion, setResultVersion] = createSignal(0)
      const messages = [claudeToolUse('tu1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        resultRevision: () => ({ id: 'result', seq: 2n, contentVersion: resultVersion(), supplementalRevision: 0n }),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tu1')!
      expect(before.category.kind).toBe('tool_use')
      expect(before.freshness.revisionKey).toMatch(/~result=\d+:result\|\d+\|0\|0$/)
      const beforeHeightKey = heightKeyForEntry(before, 0)

      setResultVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('tu1')!
      expect(after.freshness.revisionKey).toMatch(/~result=\d+:result\|\d+\|1\|0$/)
      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  it('rebuilds a tool_use entry when its paired hidden result identity changes at the same content version', () => {
    createRoot((dispose) => {
      const [resultRevision, setResultRevision] = createSignal({ id: 'result-a', seq: 5n, contentVersion: 0, supplementalRevision: 0n })
      const messages = [claudeToolUse('tu1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        resultRevision: () => resultRevision(),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tu1')!
      const beforeHeightKey = heightKeyForEntry(before, 0)

      setResultRevision({ id: 'result-b', seq: 7n, contentVersion: 0, supplementalRevision: 0n })
      cache.visibleEntries()
      const after = cache.getEntry('tu1')!

      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  it('does not rebuild a request row when an unrelated request revision changes', () => {
    createRoot((dispose) => {
      const [unrelatedVersion, setUnrelatedVersion] = createSignal(0)
      const messages = [claudeToolUse('current-request', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => ({ id: 'selected-request', seq: 1n, contentVersion: unrelatedVersion(), supplementalRevision: 0n }),
        showHiddenMessages: () => false,
      })

      cache.visibleEntries()
      const before = cache.getEntry('current-request')!
      expect(before.freshness.revisionKey).not.toContain('~request=')
      setUnrelatedVersion(1)
      cache.visibleEntries()
      expect(cache.getEntry('current-request')).toBe(before)
      dispose()
    })
  })

  it('rebuilds a non-selected tool row when its own revision changes', () => {
    createRoot((dispose) => {
      const [ownVersion, setOwnVersion] = createSignal(0)
      const messages = [claudeToolUse('current-request', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => ({ id: 'selected-request', seq: 1n, contentVersion: 0, supplementalRevision: 0n }),
        resultRevision: () => ({ id: 'selected-result', seq: 3n, contentVersion: 0, supplementalRevision: 0n }),
        contentVersionById: () => ownVersion(),
        showHiddenMessages: () => false,
      })

      cache.visibleEntries()
      const before = cache.getEntry('current-request')!
      setOwnVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('current-request')!
      expect(after).not.toBe(before)
      expect(after.freshness.revisionKey).toContain('own=15:current-request|2|1|0')
      dispose()
    })
  })

  // A result-side SUPPLEMENT is the one change that reaches the request row from
  // the other side of the span: the merged result body can change what the
  // request row renders, so the request's own row rebuilds off a revision it
  // does not hold.
  it('rebuilds a tool_use entry when its paired result\'s supplemental revision bumps', () => {
    createRoot((dispose) => {
      const [resultSupplement, setResultSupplement] = createSignal(0n)
      const messages = [claudeToolUse('tu1', 1n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        resultRevision: () => ({ id: 'result', seq: 2n, contentVersion: 0, supplementalRevision: resultSupplement() }),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tu1')!
      expect(before.freshness.revisionKey).toMatch(/~result=\d+:result\|2\|0\|0$/)
      setResultSupplement(1n)
      cache.visibleEntries()
      const after = cache.getEntry('tu1')!
      expect(after.freshness.revisionKey).toMatch(/~result=\d+:result\|2\|0\|1$/)
      expect(after).not.toBe(before)
      dispose()
    })
  })

  it('rebuilds a tool_use entry and height key when its paired hidden result arrives', () => {
    createRoot((dispose) => {
      // Result arrival is distinct from result content changing: a Task* request may
      // first classify with no hidden result and later gain one under the same id/seq.
      // The result member's arrival must invalidate the cached request entry and height key.
      const [hasResult, setHasResult] = createSignal(false)
      const messages = [claudeToolUse('tu1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        resultRevision: () => hasResult() ? { id: 'result', seq: 2n, contentVersion: 0, supplementalRevision: 0n } : undefined,
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tu1')!
      expect(before.category.kind).toBe('tool_use')
      expect(before.freshness.revisionKey).not.toContain('~result=')
      const beforeHeightKey = heightKeyForEntry(before, 0)

      setHasResult(true)
      cache.visibleEntries()
      const after = cache.getEntry('tu1')!
      expect(after.freshness.revisionKey).toContain('~result=')
      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  // The preparation MERGES the supplemental content before it classifies, so a
  // supplement that arrives late can move the CATEGORY, not just the body -- a ZCode
  // `scheduled` ExitPlanMode call is the case: its plan rides the supplemental stream,
  // and the row is a tool call until the plan lands and an assistant plan after. The
  // cached entry must follow the resolved classification, not freeze on the raw one.
  it('rebuilds an entry when the resolved classification changes', () => {
    createRoot((dispose) => {
      // A signal, because the swap must WAKE the memo: the supplement arrives as a
      // store write, and a plain array mutation is invisible to a tracked read.
      const [messages, setMessages] = createSignal([zcodeScheduledExitPlanMode()])
      const cache = createTestClassifiedEntryCache({
        messages: () => messages(),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('z1')!
      expect(before.category.kind).toBe('tool_use')
      expect(before.freshness.revisionKey).toMatch(/own=\d+:z1\|\d+\|0\|0\d*$/)
      const beforeHeightKey = heightKeyForEntry(before, 0)

      // The supplement lands: same id, same seq, a bumped supplemental revision. The
      // freshness check rebuilds the entry, and the rebuilt entry classifies the
      // MERGED payload -- tool_use before, assistant_plan after.
      setMessages([zcodeScheduledExitPlanMode('Ship it')])
      cache.visibleEntries()
      const after = cache.getEntry('z1')!
      expect(after.category.kind).toBe('assistant_plan')
      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  // The Raw JSON view reads `original` and every display reads `resolved`, and the
  // two are load-bearing APART: the merge that turns this row into a plan card must
  // never leak back into the bytes the worker stored.
  it('keeps the raw JSON parse on the original message beside the resolved one', () => {
    createRoot((dispose) => {
      const messages = [zcodeScheduledExitPlanMode('Ship it')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const entry = cache.getEntry('z1')!
      expect(entry.category.kind).toBe('assistant_plan')
      // The resolved payload carries the supplement-merged arguments...
      const resolved = entry.resolved.parentObject as { payload: { input: { plan?: string } } }
      expect(resolved.payload.input.plan).toBe('Ship it')
      // ...and the original keeps the stored bytes, with the arguments still absent.
      const original = entry.original.parentObject as { payload: { input: Record<string, unknown> } }
      expect(original.payload.input).toEqual({})
      expect(original.payload.input.plan).toBeUndefined()
      dispose()
    })
  })

  it('rebuilds a forwarded row when the tab\'s parent link hydrates', () => {
    createRoot((dispose) => {
    // A subagent tab is placed BEFORE listAgents hydrates its parentAgentId, and
    // the child's own messages are subscribed immediately -- so a forwarded row
    // can be classified while isChildTranscript still reads false. Inside the
    // child's transcript that row is an ordinary user message; in the parent's it
    // is the prompt SENT to the subagent. Without this dimension the row froze on
    // the pre-hydration answer and rendered as a collapsed "Prompt" card forever.
      const [isChild, setIsChild] = createSignal(false)
      const messages = [forwardedUserText('fu1', 3n, 'toolu_spawn')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        showHiddenMessages: () => false,
        isChildTranscript: () => isChild(),
      })
      cache.visibleEntries()
      const before = cache.getEntry('fu1')!
      expect(before.category.kind).toBe('agent_prompt')
      expect(before.freshness.isChildTranscript).toBe(false)
      const beforeHeightKey = heightKeyForEntry(before, 0)

      setIsChild(true)
      cache.visibleEntries()
      const after = cache.getEntry('fu1')!
      expect(after.freshness.isChildTranscript).toBe(true)
      expect(after.category.kind).toBe('user_text')
      expect(after).not.toBe(before)
      expect(heightKeyForEntry(after, 0)).not.toBe(beforeHeightKey)
      dispose()
    })
  })

  it('does not consult the request version for a non-tool_result row (no spurious rebuild)', () => {
    createRoot((dispose) => {
      // An assistant_text row never sizes from a request, so its request-version
      // probe must be skipped entirely -- a bump there must NOT rebuild it.
      let requestProbeReads = 0
      let resultProbeReads = 0
      const [requestVersion, setRequestVersion] = createSignal(0)
      const [resultVersion, setResultVersion] = createSignal(0)
      const messages = [assistantText('a1', 1n, 'hi')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => {
          requestProbeReads++
          return { id: 'request', seq: 1n, contentVersion: requestVersion(), supplementalRevision: 0n }
        },
        resultRevision: () => {
          resultProbeReads++
          return { id: 'result', seq: 2n, contentVersion: resultVersion(), supplementalRevision: 0n }
        },
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('a1')!
      expect(before.freshness.revisionKey).not.toContain('~request=')
      const readsAfterFirst = requestProbeReads
      const resultReadsAfterFirst = resultProbeReads
      setRequestVersion(1)
      setResultVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('a1')!
      // Same reference: the assistant row never read the request version, so the
      // bump didn't wake/rebuild it. The probe count also never advanced.
      expect(after).toBe(before)
      expect(requestProbeReads).toBe(readsAfterFirst)
      expect(resultProbeReads).toBe(resultReadsAfterFirst)
      dispose()
    })
  })

  it('records no request revision on a tool_use row, whose own span resolves to itself', () => {
    createRoot((dispose) => {
      // The resolver answers a tool_use row's own span WITH that row. Recording
      // it as the row's sibling request tracks the row's own content version a
      // second time, in a slot the height key reads as a sibling's.
      const [ownVersion, setOwnVersion] = createSignal(0)
      const messages = [claudeToolUse('tu1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => ({ id: 'tu1', seq: 2n, contentVersion: ownVersion(), supplementalRevision: 0n }),
        contentVersionById: () => ownVersion(),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('tu1')!
      expect(before.category.kind).toBe('tool_use')
      expect(before.freshness.revisionKey).toContain('own=3:tu1|2|0|0')
      expect(before.freshness.revisionKey).not.toContain('~request=')

      setOwnVersion(1)
      cache.visibleEntries()
      const after = cache.getEntry('tu1')!
      // The row rebuilds off its OWN content version, and off that alone.
      expect(after).not.toBe(before)
      expect(after.freshness.revisionKey).toContain('own=3:tu1|2|1|0')
      expect(after.freshness.revisionKey).not.toContain('~request=')
      dispose()
    })
  })

  it('records no request revision on a spanned row that is not a tool_result', () => {
    createRoot((dispose) => {
      // A Codex reasoning row carries a span but never sizes itself from an
      // request, so the request's version is not one of its freshness dimensions.
      const [requestVersion, setRequestVersion] = createSignal(0)
      const messages = [emptyCodexReasoning('r1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        requestRevision: () => ({ id: 'request', seq: 1n, contentVersion: requestVersion(), supplementalRevision: 0n }),
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const before = cache.getEntry('r1')!
      expect(before.category.kind).not.toBe('tool_result')
      expect(before.freshness.revisionKey).not.toContain('~request=')

      setRequestVersion(1)
      cache.visibleEntries()
      expect(cache.getEntry('r1')).toBe(before)
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
      const cache = createTestClassifiedEntryCache({
        messages,
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
      const cache = createTestClassifiedEntryCache({
        messages,
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

  it('prunes entries no longer in the window and reuses the cached ref for an unchanged row', () => {
    createRoot((dispose) => {
      const [messages, setMessages] = createSignal([assistantText('a1', 1n, 'hi'), assistantText('a2', 2n, 'yo')])
      const cache = createTestClassifiedEntryCache({
        messages,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()
      expect(first.map(e => e.message.id)).toEqual(['a1', 'a2'])
      const a1Entry = first[0]
      expect(cache.getEntry('a1')).toBe(a1Entry)

      // Drop a2 from the window; a1 is a new instance with the same id+seq.
      setMessages([assistantText('a1', 1n, 'hi')])
      const second = cache.visibleEntries()
      expect(second.map(e => e.message.id)).toEqual(['a1'])
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
      const cache = createTestClassifiedEntryCache({
        messages,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()[0]
      // `?.` is the type-level guard alone; the toBe below fails just as hard without an entry.
      expect(first?.freshness.revisionKey).toContain('own=2:a1|1|')
      setMessages([assistantText('a1', 7n, 'hi')]) // same id, new seq
      const second = cache.visibleEntries()[0]
      expect(second).not.toBe(first) // rebuilt off the seq change
      expect(second?.freshness.revisionKey).toContain('own=2:a1|7|')
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
      const cache = createTestClassifiedEntryCache({
        messages,
        contentVersionById: id => versions.get(id) ?? 0,
        showHiddenMessages: () => false,
      })
      const first = cache.visibleEntries()[0]
      // `?.` is the type-level guard alone; an absent entry fails the compares below.
      const firstText = JSON.stringify(first?.original.parentObject)

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
      expect(JSON.stringify(second?.original.parentObject)).not.toBe(firstText) // reflects the new body
      dispose()
    })
  })

  it('shows hidden entries when showHiddenMessages is on', () => {
    createRoot((dispose) => {
      const [showHidden, setShowHidden] = createSignal(false)
      const messages = [assistantText('a1', 1n, 'hi'), emptyCodexReasoning('r1', 2n, 'span-1')]
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
        showHiddenMessages: showHidden,
      })
      expect(cache.visibleEntries().map(e => e.message.id)).toEqual(['a1']) // r1 hidden
      setShowHidden(true)
      expect(cache.visibleEntries().map(e => e.message.id)).toEqual(['a1', 'r1'])
      dispose()
    })
  })

  it('hasVisibleEntries reports presence without depending on visibleEntries()', () => {
    createRoot((dispose) => {
      const visible = createTestClassifiedEntryCache({
        messages: () => [assistantText('a1', 1n, 'hi')],
        showHiddenMessages: () => false,
      })
      expect(visible.hasVisibleEntries()).toBe(true)

      const allHidden = createTestClassifiedEntryCache({
        messages: () => [emptyCodexReasoning('r1', 1n, 'span-1')],
        showHiddenMessages: () => false,
      })
      expect(allHidden.hasVisibleEntries()).toBe(false)
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
      const cache = createTestClassifiedEntryCache({
        messages: () => messages,
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

/**
 * Two keys, two questions.
 *
 * The virtualizer asks "must this row be re-measured", which a per-row expand or
 * diff-view toggle answers yes to. The render cache asks "must this row be read
 * again", which the same toggle answers no to. Deriving the second key from the
 * first threw away the row's extracted model, its normalized command body, its Myers
 * diff and its rendered markdown on every click of the expand control.
 */
describe('renderKeyForEntry', () => {
  function entryOf(id: string, seq: bigint, text: string) {
    return createRoot((dispose) => {
      const cache = createTestClassifiedEntryCache({
        messages: () => [assistantText(id, seq, text)],
        showHiddenMessages: () => false,
      })
      cache.visibleEntries()
      const entry = cache.getEntry(id)!
      dispose()
      return entry
    })
  }

  it('answers the same key across a UI-version bump, which the height key does not', () => {
    const entry = entryOf('m1', 1n, 'hello')
    expect(renderKeyForEntry(entry)).toBe(renderKeyForEntry(entry))
    expect(heightKeyForEntry(entry, 0)).not.toBe(heightKeyForEntry(entry, 1))
  })

  it('separates two rows by message id, so one row cannot read another\'s cache', () => {
    // Both rows carry the same seq and the same content signals, so the content key
    // alone would collide. The id is what keeps the two caches apart.
    const first = entryOf('m1', 1n, 'hello')
    const second = entryOf('m2', 1n, 'hello')
    expect(renderKeyForEntry(first)).not.toBe(renderKeyForEntry(second))
  })

  it('answers a new key when the row\'s own revision key moves', () => {
    const before = entryOf('m1', 1n, 'hello')
    const after = { ...before, freshness: { ...before.freshness, revisionKey: `${before.freshness.revisionKey}~request=1:x|1|0|0` } }
    expect(renderKeyForEntry(after)).not.toBe(renderKeyForEntry(before))
  })

  it('answers a new key when the child-transcript flag moves', () => {
    const before = entryOf('m1', 1n, 'hello')
    const after = { ...before, freshness: { ...before.freshness, isChildTranscript: true } }
    expect(renderKeyForEntry(after)).not.toBe(renderKeyForEntry(before))
  })

  // A new seq under a stable id is a different message instance -- a reseq, or a
  // notification consolidation -- so the row must be read again.
  it('answers a new key when the message seq moves', () => {
    expect(renderKeyForEntry(entryOf('m1', 2n, 'hello'))).not.toBe(renderKeyForEntry(entryOf('m1', 1n, 'hello')))
  })
})
