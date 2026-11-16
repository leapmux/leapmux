import type { AttachmentCapabilities } from '../registry'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { isFinishedToolCallStatus } from '~/components/chat/model/toolCallStatus'
import { ACP_SUPPLEMENT_IDENTITY } from '~/generated/contracts/acp-protocol'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { renderMessageContent } from '~/test-support/messageContentRenderer'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { classifyMessage } from '../../messageClassifier'
import { extractChatRow, extractedRow } from '../../rowExtraction'
import { providerFor, resolveMessageForRendering } from '../registry'
import { input } from '../testUtils'

/** Verify the common Agent Client Protocol classification and attachment contract. */
export function describeACPProviderBasics(provider: AgentProvider, attachments: AttachmentCapabilities): void {
  const plugin = providerFor(provider)!

  it('exposes attachment capabilities', () => {
    expect(plugin?.configuration?.attachments).toEqual(attachments)
  })

  // The worker joins a run of agent_message_chunk / agent_thought_chunk updates into
  // ONE assembled row, so no chunk ever reaches the browser. The shared classifier
  // answers that row before the plugin runs, and the plugin must leave it alone --
  // a plugin that claimed it too would draw the same text twice.
  it.each([
    ['text', 'assistant_text'],
    ['reasoning', 'assistant_thinking'],
  ] as const)('leaves an assembled %s row to the shared classifier', (kind, expected) => {
    const parent = assembledMessageRow(kind, 'Hello')
    expect(classifyMessage(input(parent, null, provider))).toEqual({ kind: expected })
    expect(plugin?.transcript.extractRow!({
      resolved: input(parent, null, provider),
      category: { kind: expected },
      span: {
        request: undefined,
        role: 'other',
        visibleRows: { request: false },
      },
    })).toBeNull()
  })

  it('hides config_option_update', () => {
    const parent = {
      sessionUpdate: 'config_option_update',
      configOptions: [],
    }
    expect(plugin?.transcript.classify(input(parent))).toEqual({ kind: 'hidden' })
  })
}

export function renderACPToolPair(provider: AgentProvider, request: Record<string, unknown>, result: Record<string, unknown>, supplemental?: Record<string, unknown>) {
  const start = {
    sessionUpdate: 'tool_call',
    toolCallId: 'call',
    status: 'pending',
    ...request,
  }
  const end = {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'call',
    status: 'completed',
    ...result,
  }
  const parsed = {
    ...input(end),
    supplementalContent: supplemental
      ? {
          sessionUpdate: end.sessionUpdate,
          status: end.status,
          toolCallId: end.toolCallId,
          ...supplemental,
        }
      : undefined,
  }
  const plugin = providerFor(provider)!
  const hasRequestRow = Object.keys(request).length > 0
  // Each row sits inside a FRAGMENT rather than being called bare: `render(fn)` calls
  // `fn` once and inserts the result, so a bare call freezes the row at its first
  // payload. The JSX compiler wraps an expression inside a fragment in a memo, which is
  // the reactive shape `MessageBubble` gives the dispatcher in production.
  // The request row heads itself with the newest call of its span, the result row's call,
  // as `MessageBubble` heads it (see ToolMessage). The rest of the row is its own frame.
  const resultRow = extractedRow(extractChatRow(provider, parsed, plugin.transcript.classify(parsed), {
    span: {
      request: input(start),
      role: 'result',
      visibleRows: { request: hasRequestRow },
    },
  }))
  const toolHeader = resultRow?.kind === 'tool' && resultRow.role === 'result'
    ? resultRow.call
    : undefined
  return render(() => [
    Object.keys(request).length > 0 && (
      <>
        {renderMessageContent(start, {
          premeasureMode: true,
          toolHeader,
          sources: testMessageSources({
            current: () => input(start),
            role: () => 'request',
            visibleRows: () => ({ request: hasRequestRow }),
          }),
        }, plugin?.transcript.classify(input(start)), provider)}
      </>
    ),
    <>
      {renderMessageContent(end, {
        premeasureMode: true,
        sources: testMessageSources({
          current: () => parsed,
          request: () => input(start),
          role: () => 'result',
          visibleRows: () => ({ request: hasRequestRow }),
        }),
      }, plugin?.transcript.classify(parsed), provider)}
    </>,
  ])
}

/**
 * Render ONE Agent Client Protocol row the way production does.
 *
 * Through `renderMessageContent`, not the plugin: the dispatcher is what resolves the
 * extraction and wraps it in the shared completion chrome, so a test that called the
 * plugin directly could pass while the mounted row drew nothing.
 */
export function renderACPRow(provider: AgentProvider, parsed: Record<string, unknown>): ReturnType<typeof render> {
  const plugin = providerFor(provider)!
  // The fragment is load-bearing; see `renderACPToolPair` below.
  return render(() => <>{renderMessageContent(parsed, undefined, plugin?.transcript.classify(input(parsed)), provider)}</>)
}

export function acpTextContent(text: string) {
  return [
    {
      type: 'content',
      content: { type: 'text', text },
    },
  ]
}

/**
 * One call whose RESULT states a fact that its request does not, read as the two rows
 * that the transcript draws for it.
 *
 * Each supplement takes the identity of its own frame (the session update, the call id
 * and the status), which the identity gate of a supplement requires.
 */
export interface ACPCallRowsCase {
  /** What the case shows, for the test titles. */
  name: string
  /** The frame that opens the call. */
  request: Record<string, unknown>
  /** The supplement that the worker stored beside the request, if any. */
  requestSupplement?: Record<string, unknown>
  /** The frame that closes the call. */
  result: Record<string, unknown>
  /** The supplement that the worker stored beside the result, if any. */
  resultSupplement?: Record<string, unknown>
  /** What the request row's call states, from its own frame alone. */
  requestCall: Record<string, unknown>
  /** What the result row's call states, from its own frame plus the request. */
  resultCall: Record<string, unknown>
}

/** One stored row of a case: the frame, plus its supplement under the frame's identity. */
function storedACPRow(frame: Record<string, unknown>, supplement: Record<string, unknown> | undefined): ParsedMessageContent {
  const identity = Object.fromEntries(Object.values(ACP_SUPPLEMENT_IDENTITY)
    .filter(key => Object.hasOwn(frame, key))
    .map(key => [key, frame[key]]))
  return {
    rawText: '',
    topLevel: frame,
    parentObject: frame,
    wrapper: null,
    ...(supplement === undefined
      ? {}
      : {
          supplementalContent: {
            ...identity,
            ...supplement,
          },
        }),
  }
}

/**
 * Verify that one provider builds each row of a call from what the row may read.
 *
 * - The request row reads its own frame alone. Its call holds no result and no
 *   finished status, and it needs no other message of its span.
 * - The result row reads its own frame plus the request, so its call carries the whole
 *   call, also each fact that only the result states.
 */
export function describeACPCallRows(provider: AgentProvider, cases: readonly ACPCallRowsCase[]): void {
  describe('the request and result rows of one call', () => {
    it.each(cases)('asks for the request on the result row alone: $name', (rows) => {
      const plugin = providerFor(provider)!
      const request = resolveMessageForRendering(storedACPRow(rows.request, rows.requestSupplement), provider)
      const result = resolveMessageForRendering(storedACPRow(rows.result, rows.resultSupplement), provider)
      expect(plugin.transcript.needsRequest?.(request)).toBe(false)
      expect(plugin.transcript.needsRequest?.(result)).toBe(true)
    })

    it.each(cases)('builds the request row from its own frame, with no result: $name', (rows) => {
      const stored = storedACPRow(rows.request, rows.requestSupplement)
      const call = providerToolCall(provider, rows.request, {
        role: 'request',
        request: stored,
        ...(stored.supplementalContent === undefined
          ? {}
          : { supplementalContent: stored.supplementalContent }),
      })
      expect(call).not.toBeNull()
      expect(call?.result).toBeUndefined()
      expect(call && isFinishedToolCallStatus(call.status)).toBe(false)
      expect(call).toMatchObject(rows.requestCall)
    })

    it.each(cases)('builds the whole call on the result row: $name', (rows) => {
      const stored = storedACPRow(rows.result, rows.resultSupplement)
      const call = providerToolCall(provider, rows.result, {
        role: 'result',
        request: storedACPRow(rows.request, rows.requestSupplement),
        ...(stored.supplementalContent === undefined
          ? {}
          : { supplementalContent: stored.supplementalContent }),
      })
      expect(call).toMatchObject(rows.resultCall)
    })
  })
}
