import type { ResolvedMessage } from './messageContextResolver'
import type { ChatRowExtraction } from './rowExtraction'
import type { RowExtractionContext } from './rowModelCache'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { create } from '@bufbuild/protobuf'
import { createMemo, createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { ControlDecisionSchema, ControlPromptSchema } from '~/generated/proto/leapmux/v1/agent_control_pb'
import { AgentProvider, AvailableOptionGroupSchema } from '~/generated/proto/leapmux/v1/agent_pb'
import { clearSettingsLabelCache, updateSettingsLabelCache } from '~/lib/settingsLabelCache'
import { makeControlResponseMessage, makeMessage, makeTranscriptMessage, rawContent } from '~/test-support/messageFactory'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { resolveMessageForRendering } from './providers/registry'
import { extractedRow } from './rowExtraction'
import { cachedChatRow, cachedSpanHeaderCall } from './rowModelCache'
import { prepareMessage } from './rowPreparation'
import './providers'

function parsed(parentObject: Record<string, unknown>): ParsedMessageContent {
  return {
    rawText: JSON.stringify(parentObject),
    topLevel: parentObject,
    parentObject,
    wrapper: null,
  }
}

describe('rowModelCache', () => {
  it('selects the tool block addressed by the stored span', () => {
    const content = resolveMessageForRendering(parsed({
      type: 'assistant',
      session_id: 'qoder-session',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: 'first',
            name: 'Bash',
            input: { command: 'first command' },
          },
          {
            type: 'tool_use',
            id: 'second',
            name: 'Bash',
            input: { command: 'second command' },
          },
        ],
      },
    }), AgentProvider.QODER)
    const context = { spanId: 'second', spanType: 'Bash' }

    expect(extractedRow(cachedChatRow(context, AgentProvider.QODER, content, { kind: 'tool_use' }, undefined))).toMatchObject({
      kind: 'tool',
      call: { id: 'second' },
    })
  })

  it('passes the stored span identity and rebuilds a row when that identity changes', () => {
    const native = resolveMessageForRendering(parsed({
      type: 'session.tool.success',
      data: {
        sessionID: 'ses_1',
        assistantMessageID: 'msg_1',
        id: 'native-call',
        name: 'shell',
        input: { command: 'pwd' },
        content: [
          { type: 'text', text: 'output' },
        ],
      },
    }), AgentProvider.OPENCODE)
    const context = {
      spanId: 'stored-span',
      renderCache: createMessageRenderCacheStore().forRow('same-revision'),
    }
    const first = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, native, { kind: 'tool_result' }, undefined))
    expect(first?.kind === 'tool'
      ? first.call.id
      : undefined).toBe('stored-span')
    context.spanId = 'changed-span'
    const second = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, native, { kind: 'tool_result' }, undefined))
    expect(second?.kind === 'tool'
      ? second.call.id
      : undefined).toBe('changed-span')
    expect(second).not.toBe(first)
  })

  it('rebuilds a row when its resolved sibling becomes visible', () => {
    const request = resolveMessageForRendering(parsed({
      sessionUpdate: 'tool_call',
      toolCallId: 'call',
      status: 'pending',
      kind: 'execute',
      title: 'Run checks',
      rawInput: { command: 'bun test' },
    }), AgentProvider.KILO)
    const result = resolveMessageForRendering(parsed({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call',
      status: 'completed',
      content: [
        {
          type: 'content',
          content: { type: 'text', text: 'passed' },
        },
      ],
    }), AgentProvider.KILO)
    let requestVisible = false
    const context = {
      renderCache: createMessageRenderCacheStore().forRow('result-revision'),
      sources: {
        request: () => request,
        role: () => 'result' as const,
        visibleRows: () => ({ request: requestVisible }),
      },
      spanType: 'execute',
    } as unknown as RowExtractionContext

    const first = extractedRow(cachedChatRow(context, AgentProvider.KILO, result, { kind: 'tool_use' }, undefined))
    expect(first?.kind === 'tool'
      ? first.hasRequestRow
      : undefined).toBe(false)

    requestVisible = true
    const second = extractedRow(cachedChatRow(context, AgentProvider.KILO, result, { kind: 'tool_use' }, undefined))
    expect(second?.kind === 'tool'
      ? second.hasRequestRow
      : undefined).toBe(true)
    expect(second).not.toBe(first)
  })

  // A tab draws its saved rows before the agent's settings reach the browser, so a
  // label that a row reads can arrive after the cache keeps the row.
  describe('a row that reads a settings label', () => {
    afterEach(() => clearSettingsLabelCache())

    /** A saved approval of a Droid plan that switched the permission mode to `auto-medium`. */
    function droidPlanApproval() {
      return prepareMessage(makeControlResponseMessage(AgentProvider.DROID, { approved: true }, undefined, {
        prompt: create(ControlPromptSchema, {
          kind: { case: 'plan', value: {} },
        }),
        decision: create(ControlDecisionSchema, {
          kind: {
            case: 'plan',
            value: { approve: true, permissionMode: 'auto-medium' },
          },
        }),
      }))
    }

    function droidModeLabels(): void {
      updateSettingsLabelCache(AgentProvider.DROID, [
        create(AvailableOptionGroupSchema, {
          id: 'permissionMode',
          options: [
            { id: 'auto-medium', name: 'Auto (Medium)' },
          ],
        }),
      ])
    }

    function answerText(extraction: ChatRowExtraction): string | undefined {
      const row = extractedRow(extraction)
      return row?.kind === 'control-response' && row.display.kind === 'label'
        ? row.display.text
        : undefined
    }

    function cachedApproval() {
      const prepared = droidPlanApproval()
      const context = {
        renderCache: createMessageRenderCacheStore().forRow('approval-revision'),
      }
      return () => cachedChatRow(context, AgentProvider.DROID, prepared.resolved, prepared.category, undefined)
    }

    it('extracts the row again when the label that it read arrives', () => {
      const read = cachedApproval()
      expect(answerText(read())).toBe('Approve (auto-medium)')
      droidModeLabels()
      expect(answerText(read())).toBe('Approve (Auto (Medium))')
    })

    it('keeps the row object while the labels that it read are unchanged', () => {
      const read = cachedApproval()
      const first = read()
      expect(read()).toBe(first)
      updateSettingsLabelCache(AgentProvider.DROID, [
        create(AvailableOptionGroupSchema, {
          id: 'model',
          options: [
            { id: 'droid-model', name: 'Droid Model' },
          ],
        }),
      ])
      expect(read()).toBe(first)
    })

    // MessageBubble reads its row in a memo, which must run again by itself when the
    // label arrives: nothing else about the row changes.
    it('runs a computation that read the row again when the label arrives', () => {
      createRoot((dispose) => {
        const read = cachedApproval()
        const text = createMemo(() => answerText(read()))
        expect(text()).toBe('Approve (auto-medium)')
        droidModeLabels()
        expect(text()).toBe('Approve (Auto (Medium))')
        dispose()
      })
    })
  })
})

describe('cachedSpanHeaderCall', () => {
  function resolvedOf(message: AgentChatMessage): ResolvedMessage {
    const prepared = prepareMessage(message)
    return {
      message,
      original: prepared.original,
      resolved: prepared.resolved,
      revision: {
        id: message.id,
        seq: message.seq,
        contentVersion: 0,
        supplementalRevision: 0n,
      },
    }
  }

  /** A Claude Bash call: its request asks for `make`, and its result states the output. */
  function bashSpan(): { request: ResolvedMessage, result: ResolvedMessage } {
    return {
      request: resolvedOf(makeMessage({
        id: 'bash-request',
        seq: 1n,
        spanId: 'toolu_bash',
        content: rawContent({
          type: 'assistant',
          message: {
            content: [
              {
                type: 'tool_use',
                id: 'toolu_bash',
                name: 'Bash',
                input: { command: 'make', description: 'Build it' },
              },
            ],
          },
        }),
      })),
      result: resolvedOf(makeMessage({
        id: 'bash-result',
        seq: 2n,
        spanId: 'toolu_bash',
        content: rawContent({
          type: 'user',
          message: {
            content: [
              { type: 'tool_result', tool_use_id: 'toolu_bash', content: 'built' },
            ],
          },
        }),
      })),
    }
  }

  it('reads the call that the result row draws', () => {
    const { request, result } = bashSpan()
    const call = cachedSpanHeaderCall(result, request.resolved)
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('completed')
  })

  // Every copy of the request row asks for the same header.
  it('extracts the result once for one revision of each side', () => {
    const { request, result } = bashSpan()
    expect(cachedSpanHeaderCall(result, request.resolved)).toBe(cachedSpanHeaderCall(result, request.resolved))
  })

  it('shares extraction with the result row when their inputs match', () => {
    const { request, result } = bashSpan()
    const header = cachedSpanHeaderCall(result, request.resolved)
    const context = {
      spanId: result.message.spanId,
      spanType: result.message.spanType,
      sources: {
        request: () => request.resolved,
        role: () => 'result' as const,
        visibleRows: () => ({ request: true }),
      },
    }
    const body = extractedRow(cachedChatRow(context, result.message.agentProvider, result.resolved, prepareMessage(result.message).category, result.message.completion))
    expect(body?.kind).toBe('tool')
    if (body?.kind === 'tool')
      expect(body.call).toBe(header)
  })

  it('extracts again for a new revision of a side', () => {
    const { request, result } = bashSpan()
    const first = cachedSpanHeaderCall(result, request.resolved)
    const revised = bashSpan()
    const again = cachedSpanHeaderCall(result, revised.request.resolved)
    expect(again).not.toBe(first)
    expect(again).toEqual(first)
    expect(cachedSpanHeaderCall(revised.result, request.resolved)).not.toBe(first)
  })

  it('reads no header from a message that draws no result row', () => {
    const { request } = bashSpan()
    expect(cachedSpanHeaderCall(request, request.resolved)).toBeUndefined()
  })

  describe('a header that reads a settings label', () => {
    afterEach(() => clearSettingsLabelCache())

    /** A Codex spawn on the `gpt-x` model: its running request, and its finished result. */
    function codexSpawnSpan(): { request: ResolvedMessage, result: ResolvedMessage } {
      const frame = (id: string, seq: bigint, status: string) => resolvedOf(makeTranscriptMessage({
        id,
        provider: AgentProvider.CODEX,
        spanId: 'spawn-call',
        spanType: 'collabAgentToolCall',
        agentSessionId: 'codex-session',
        content: {
          item: {
            id: 'spawn-call',
            type: 'collabAgentToolCall',
            tool: 'spawnAgent',
            prompt: 'Inspect the code.',
            model: 'gpt-x',
            status,
            receiverThreadIds: ['child-1'],
          },
        },
      }, seq))
      return {
        request: frame('spawn-request', 1n, 'inProgress'),
        result: frame('spawn-result', 2n, 'completed'),
      }
    }

    function modelOf(result: ResolvedMessage, request: ResolvedMessage): string | undefined {
      const call = cachedSpanHeaderCall(result, request.resolved)
      return call?.kind === 'agent'
        ? call.request.metadata?.find(entry => entry.label === 'Model')?.value
        : undefined
    }

    it('extracts the header again when the label that it read arrives', () => {
      const { request, result } = codexSpawnSpan()
      expect(modelOf(result, request)).toBe('gpt-x')
      updateSettingsLabelCache(AgentProvider.CODEX, [
        create(AvailableOptionGroupSchema, {
          id: 'model',
          options: [
            { id: 'gpt-x', name: 'GPT X' },
          ],
        }),
      ])
      expect(modelOf(result, request)).toBe('GPT X')
    })

    // The result row reuses the extraction that the header made first. Its own cache entry
    // must still learn the label that the shared extraction read, or it keeps the old words.
    it('follows the labels of an extraction that the result row shares with the header', () => {
      const { request, result } = codexSpawnSpan()
      expect(modelOf(result, request)).toBe('gpt-x')
      const context = {
        renderCache: createMessageRenderCacheStore().forRow('spawn-result-revision'),
        spanId: result.message.spanId,
        spanType: result.message.spanType,
        sources: {
          request: () => request.resolved,
          role: () => 'result' as const,
          visibleRows: () => ({ request: true }),
        },
      }
      const category = prepareMessage(result.message).category
      const bodyModel = (): string | undefined => {
        const body = extractedRow(cachedChatRow(context, AgentProvider.CODEX, result.resolved, category, result.message.completion))
        return body?.kind === 'tool' && body.call.kind === 'agent'
          ? body.call.request.metadata?.find(entry => entry.label === 'Model')?.value
          : undefined
      }
      expect(bodyModel()).toBe('gpt-x')

      updateSettingsLabelCache(AgentProvider.CODEX, [
        create(AvailableOptionGroupSchema, {
          id: 'model',
          options: [
            { id: 'gpt-x', name: 'GPT X' },
          ],
        }),
      ])
      expect(bodyModel()).toBe('GPT X')
    })

    it('keeps the header call while the labels that it read are unchanged', () => {
      const { request, result } = codexSpawnSpan()
      const first = cachedSpanHeaderCall(result, request.resolved)
      updateSettingsLabelCache(AgentProvider.CODEX, [
        create(AvailableOptionGroupSchema, {
          id: 'collaboration_mode',
          options: [
            { id: 'plan', name: 'Plan' },
          ],
        }),
      ])
      expect(cachedSpanHeaderCall(result, request.resolved)).toBe(first)
    })
  })
})
