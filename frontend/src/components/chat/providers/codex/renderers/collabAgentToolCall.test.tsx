import { fireEvent, render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../../../messageRenderers'
import { providerFor } from '../../registry'
import { input, toolMessageInput } from '../../testUtils'
import { codexAgentRequest, codexAgentResults } from '../extractors/agent'
import '../plugin'
import '../../testMocks'

function renderItem(fields: Record<string, unknown>) {
  const parsed = { item: { id: 'call', type: 'collabAgentToolCall', status: 'completed', ...fields } }
  const plugin = providerFor(AgentProvider.CODEX)!
  return render(() => renderMessageContent(parsed, { premeasureMode: true, spanType: 'collabAgentToolCall' }, plugin.classify(input(parsed)), AgentProvider.CODEX))
}

describe('codex agent tool rendering', () => {
  it('recovers request-only prompt and model fields for a spawn result and its toolbar', () => {
    const request = input({ item: { id: 'call', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'inProgress', prompt: '**Inspect** the parser.', model: 'requested-model', reasoningEffort: 'high' } })
    const result = { item: { id: 'call', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed', prompt: null, model: null, receiverThreadIds: ['child'], agentsStates: { child: { status: 'running' } } } }
    const plugin = providerFor(AgentProvider.CODEX)!
    const sources = testMessageSources({ current: () => input(result), request: () => request, role: () => 'result' })
    const { container } = render(() => renderMessageContent(result, { premeasureMode: true, sources }, plugin.classify(input(result)), AgentProvider.CODEX))
    expect(container.querySelector('strong')?.textContent).toBe('Inspect')
    expect(container.textContent).toContain('requested-model')
    expect(container.textContent).toContain('high')
    const meta = plugin.toolResultMeta?.(plugin.classify(input(result)), { ...toolMessageInput(result, 'collabAgentToolCall', request), role: 'result' })
    expect(meta?.copyableContent()).toBe('**Inspect** the parser.')
  })

  it('does not pair numeric tool IDs', () => {
    const request = input({ item: { id: 3, type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'inProgress', prompt: 'Wrong prompt' } })
    const result = { item: { id: 3, type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed', receiverThreadIds: ['child'] } }
    const plugin = providerFor(AgentProvider.CODEX)!
    const { container } = render(() => renderMessageContent(result, { premeasureMode: true, sources: testMessageSources({ request: () => request, role: () => 'result' }) }, plugin.classify(input(result)), AgentProvider.CODEX))
    expect(container.textContent).toContain('Subagent')
    expect(container.textContent).not.toContain('Wrong prompt')
  })
  it('keeps completed request metadata out of the collapsed request row', () => {
    const item = { id: 'call', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'inProgress', model: 'native-model', reasoningEffort: 'high', prompt: 'Inspect the code.', receiverThreadIds: [] }
    const request = { item }
    const result = { item: { ...item, status: 'completed', receiverThreadIds: ['child'] } }
    const plugin = providerFor(AgentProvider.CODEX)!
    const sources = testMessageSources({ current: () => input(request), result: () => input(result), role: () => 'opener' })
    const { container } = render(() => renderMessageContent(request, { premeasureMode: true, sources }, plugin.classify(input(request)), AgentProvider.CODEX))
    expect(container.textContent).not.toContain('native-model')
    expect(container.textContent).not.toContain('Reasoning effort')
  })
  it('offers copy and expansion for a long child report', () => {
    const report = 'First\nSecond\nThird\nFourth\nFifth'
    const parsed = { item: { id: 'call', type: 'collabAgentToolCall', tool: 'wait', status: 'completed', agentsStates: { child: { status: 'completed', message: report } } } }
    const plugin = providerFor(AgentProvider.CODEX)!
    const meta = plugin.toolResultMeta?.(plugin.classify(input(parsed)), toolMessageInput(parsed, 'collabAgentToolCall'))
    expect(meta?.collapsible).toBe(true)
    expect(meta?.copyableContent()).toBe(report)
  })

  it('identifies an interrupted collaboration call as a result', () => {
    expect(providerFor(AgentProvider.CODEX)!.spanRole?.(input({ item: { type: 'collabAgentToolCall', id: 'call', status: 'interrupted' } }))).toBe('result')
  })

  it('preserves unfamiliar tool and state strings that match object properties', () => {
    expect(codexAgentRequest({ tool: 'toString' }).description).toBe('toString')
    expect(codexAgentResults({ agentsStates: { child: { status: '__proto__' } } })[0]).toMatchObject({ status: '__proto__', outcome: 'unknown' })
  })
  it('shows each state and report returned by a wait call', () => {
    const { container } = renderItem({ tool: 'wait', receiverThreadIds: ['one', 'two'], agentsStates: { one: { status: 'completed', message: '**Report**\n\nThe checks passed.' }, two: { status: 'errored', message: 'The worker failed.' } } })
    expect(container.textContent).toContain('Agent one completed')
    expect(container.querySelector('strong')?.textContent).toBe('Report')
    expect(container.textContent).toContain('Agent two failed')
    expect(container.textContent).toContain('The worker failed.')
  })

  it('keeps a completed spawn result visible without claiming that its child completed', () => {
    const { container } = renderItem({ tool: 'spawnAgent', receiverThreadIds: ['child'], agentsStates: { child: { status: 'pendingInit' } }, prompt: 'Inspect sample.' })
    expect(container.textContent).toContain('child')
    expect(container.textContent).toContain('starting')
    expect(container.textContent).not.toContain('child completed')
  })

  it('uses the common expandable prompt for a completed send-input request', async () => {
    const item = { id: 'call', type: 'collabAgentToolCall', tool: 'sendInput', status: 'inProgress', prompt: '**Instruction**\n\nCheck the parser.', receiverThreadIds: ['child'], agentsStates: {} }
    const request = { item }
    const result = { item: { ...item, status: 'completed' } }
    const sources = testMessageSources({ current: () => input(request), result: () => input(result), role: () => 'opener' })
    const plugin = providerFor(AgentProvider.CODEX)!
    const { container, getByRole } = render(() => renderMessageContent(request, { premeasureMode: true, sources }, plugin.classify(input(request)), AgentProvider.CODEX))
    expect(container.textContent).not.toContain('Instruction')
    await fireEvent.click(getByRole('button', { name: 'Show prompt' }))
    expect(container.querySelector('strong')?.textContent).toBe('Instruction')
  })
})
