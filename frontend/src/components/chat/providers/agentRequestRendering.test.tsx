import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../messageContentRenderer'
import { providerFor } from './registry'
import { input } from './testUtils'
import './index'
import './testMocks'

describe.each([AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.OPENCODE, AgentProvider.KILO])('shared agent request (%s)', (provider) => {
  function renderRequest(completed: boolean) {
    const args = { description: 'Inspect project structure', subagent_type: 'explore', prompt: '**Instruction**\n\n1. Read the entry points.' }
    const request = provider === AgentProvider.CLAUDE_CODE
      ? { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call', name: 'Agent', input: args }] } }
      : provider === AgentProvider.ZCODE
        ? { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'call', toolName: 'Agent', input: args } }
        : { sessionUpdate: 'tool_call', toolCallId: 'call', title: 'task', kind: 'think', status: 'pending', rawInput: args }
    // Each dialect states the SAME completion: the call ended and it reported "Done".
    // The Agent Client Protocol frame carries that word in its content blocks, and it
    // has to carry it -- a completed call states a result (invariant I2), so a frame
    // that reports nothing at all describes no answered call.
    const result = provider === AgentProvider.CLAUDE_CODE
      ? { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'call', content: 'Done' }] } }
      : provider === AgentProvider.ZCODE
        ? { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success: true, content: 'Done' } } }
        : { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'Done' } }] }
    const sources = testMessageSources({
      current: () => input(request),
      result: () => completed ? input(result) : undefined,
      role: () => 'request',
      visibleRows: () => ({ request: true, result: completed }),
    })
    return { ...render(() => renderMessageContent(request, { premeasureMode: true, sources }, providerFor(provider)!.transcript.classify(input(request)), provider)), request }
  }

  it('shows the pending prompt as formatted Markdown', () => {
    const { container } = renderRequest(false)
    expect(container.textContent).toContain('Inspect project structure (explore)')
    expect(container.querySelector('strong')?.textContent).toBe('Instruction')
    expect(container.querySelector('ol li')?.textContent).toContain('Read the entry points.')
  })

  it('requests the result even when all prompt arguments are available', () => {
    const { request } = renderRequest(false)
    expect(providerFor(provider)!.transcript.relatedMessages?.(input(request))).toContain('result')
  })

  it('keeps a completed request compact and lets the user open its prompt', async () => {
    const { container, getByRole } = renderRequest(true)
    expect(container.textContent).not.toContain('Instruction')
    await fireEvent.click(getByRole('button', { name: 'Show prompt' }))
    expect(container.querySelector('strong')?.textContent).toBe('Instruction')
  })
})
