import type { AttachmentCapabilities } from '../registry'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { expect, it } from 'vitest'
import { assembledMessageRow } from '~/test-support/assembledMessages'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { classifyMessage } from '../../messageClassification'
import { renderMessageContent } from '../../messageRenderers'
import { providerFor } from '../registry'
import { input } from '../testUtils'

/** Verify the common Agent Client Protocol classification and attachment contract. */
export function describeACPProviderBasics(provider: AgentProvider, attachments: AttachmentCapabilities): void {
  const plugin = providerFor(provider)!

  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual(attachments)
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
    expect(plugin.renderMessage!({ kind: expected }, parent)).toBeNull()
  })

  it('hides config_option_update', () => {
    const parent = {
      sessionUpdate: 'config_option_update',
      configOptions: [],
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'hidden' })
  })
}

export function renderACPToolPair(provider: AgentProvider, request: Record<string, unknown>, result: Record<string, unknown>, supplemental?: Record<string, unknown>) {
  const start = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...request }
  const end = { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...result }
  const parsed = { ...input(end), supplementalContent: supplemental ? { sessionUpdate: end.sessionUpdate, status: end.status, toolCallId: end.toolCallId, ...supplemental } : undefined }
  const plugin = providerFor(provider)!
  return render(() => [
    Object.keys(request).length > 0 && renderMessageContent(start, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => input(start), result: () => parsed }),
    }, plugin.classify(input(start)), provider),
    renderMessageContent(end, {
      premeasureMode: true,
      sources: testMessageSources({ current: () => parsed, request: () => input(start) }),
    }, plugin.classify(parsed), provider),
  ])
}

export const acpTextContent = (text: string) => [{ type: 'content', content: { type: 'text', text } }]
