import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { todoList } from '~/components/todo/TodoList.css'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageSources } from '~/test-support/messageRenderSources'
import { renderMessageContent } from '../messageRenderers'
import { toolUseHeader } from '../toolStyles.css'
import { providerFor } from './registry'
import { input } from './testUtils'
import './index'
import './testMocks'

describe.each([AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE])('paired to-do layout (%s)', (provider) => {
  it.each([false, true])('puts the checklist in the result row (empty: %s)', (empty) => {
    const todos = empty ? [] : [{ content: 'Inspect sample', status: 'pending' }]
    const request = provider === AgentProvider.CLAUDE_CODE
      ? { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'call', name: 'TodoWrite', input: { todos } }] } }
      : { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'call', toolName: 'TodoWrite', input: { todos } } }
    const result = provider === AgentProvider.CLAUDE_CODE
      ? { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'call', content: 'Todos updated' }] }, tool_use_result: { newTodos: todos } }
      : { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success: true, content: 'Todos updated' } } }
    const plugin = providerFor(provider)!
    const parsed = (message: Record<string, unknown>) => ({ ...input(message), spanType: 'TodoWrite' })
    const sources = testMessageSources({ request: () => parsed(request), result: () => parsed(result) })
    const { container } = render(() => (
      <>
        <div data-row="request">{renderMessageContent(request, { premeasureMode: true, spanType: 'TodoWrite', sources }, plugin.classify(parsed(request)), provider)}</div>
        <div data-row="result">{renderMessageContent(result, { premeasureMode: true, spanType: 'TodoWrite', sources }, plugin.classify(parsed(result)), provider)}</div>
      </>
    ))
    expect(container.querySelectorAll(`.${toolUseHeader}`)).toHaveLength(1)
    expect(container.querySelector(`[data-row="request"] .${todoList}`)).toBeNull()
    const body = container.querySelector('[data-row="result"]')!
    expect(body.textContent).toContain(empty ? 'To-do list cleared' : 'Inspect sample')
    expect(body.textContent).not.toContain('Todos updated')
    expect(body.querySelectorAll(`.${todoList}`)).toHaveLength(empty ? 0 : 1)
  })
})
