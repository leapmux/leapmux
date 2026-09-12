import type { AttachmentCapabilities, Provider } from '../registry'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { render } from '@solidjs/testing-library'
import { expect, it } from 'vitest'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../messageRenderers'
import { providerFor } from '../registry'
import { input } from '../testUtils'

/** Verify the common Agent Client Protocol classification and attachment contract. */
export function describeACPProviderBasics(plugin: Provider, attachments: AttachmentCapabilities): void {
  it('exposes attachment capabilities', () => {
    expect(plugin.attachments).toEqual(attachments)
  })

  it('classifies agent_message_chunk as assistant_text', () => {
    const parent = {
      sessionUpdate: 'agent_message_chunk',
      content: { type: 'text', text: 'Hello' },
    }
    expect(plugin.classify(input(parent))).toEqual({ kind: 'assistant_text' })
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
