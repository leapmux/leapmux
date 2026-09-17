import type { AttachmentCapabilities } from '../registry'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { expect, it } from 'vitest'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { classifyMessage } from '../../messageClassification'
import { renderMessageContent } from '../../rowRenderers'
import { providerFor } from '../registry'
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
      parsed: input(parent, null, provider),
      category: { kind: expected },
      sides: { current: undefined, request: undefined, result: undefined, role: 'other' },
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
  const start = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...request }
  const end = { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...result }
  const parsed = { ...input(end), supplementalContent: supplemental ? { sessionUpdate: end.sessionUpdate, status: end.status, toolCallId: end.toolCallId, ...supplemental } : undefined }
  const plugin = providerFor(provider)!
  // Each row sits inside a FRAGMENT rather than being called bare: `render(fn)` calls
  // `fn` once and inserts the result, so a bare call freezes the row at its first
  // payload. The JSX compiler wraps an expression inside a fragment in a memo, which is
  // the reactive shape `MessageBubble` gives the dispatcher in production.
  return render(() => [
    Object.keys(request).length > 0 && (
      <>
        {renderMessageContent(start, {
          premeasureMode: true,
          sources: testMessageSources({ current: () => input(start), result: () => parsed }),
        }, plugin?.transcript.classify(input(start)), provider)}
      </>
    ),
    <>
      {renderMessageContent(end, {
        premeasureMode: true,
        sources: testMessageSources({ current: () => parsed, request: () => input(start) }),
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

export const acpTextContent = (text: string) => [{ type: 'content', content: { type: 'text', text } }]
