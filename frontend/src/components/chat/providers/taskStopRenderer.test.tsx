import type { MessageCategory } from '../messageClassification'
import { render } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import './testMocks'

const { renderMessageContent } = await import('../messageRenderers')
type RenderContext = import('../messageRenderers').RenderContext

/** Construct a TaskStop tool_use assistant message. */
function makeTaskStopMessage(input: Record<string, unknown> = {}) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'test-taskstop',
        name: 'TaskStop',
        input: { task_id: 'bfbc3f6', ...input },
      }],
    },
  }
}

/** Construct a TaskStop tool_result user message. */
function makeTaskStopResult(resultContent: string) {
  return {
    type: 'user',
    message: {
      role: 'user',
      content: [{
        tool_use_id: 'test-taskstop',
        type: 'tool_result',
        content: resultContent,
      }],
    },
    tool_use_result: { tool_name: 'TaskStop' },
  }
}

/** Render a TaskStop tool_use message and return its text content. */
function renderToolUseText(input?: Record<string, unknown>, context?: RenderContext): string {
  const msg = makeTaskStopMessage(input)
  const toolUse = (msg.message.content as Array<Record<string, unknown>>)[0]
  const category: MessageCategory = { kind: 'tool_use', toolName: 'TaskStop', toolUse, content: msg.message.content as Array<Record<string, unknown>> }
  const result = renderMessageContent(msg, MessageRole.ASSISTANT, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

/** Render a TaskStop tool_result message and return its text content. */
function renderToolResultText(resultContent: string, context?: RenderContext): string {
  const msg = makeTaskStopResult(resultContent)
  const category: MessageCategory = { kind: 'tool_result' }
  const result = renderMessageContent(msg, MessageRole.USER, context, category, AgentProvider.CLAUDE_CODE)
  const { container } = render(() => result)
  return container.textContent?.trim() ?? ''
}

describe('taskStop tool_use rendering', () => {
  it('shows "Stop task" with task_id in header', () => {
    const text = renderToolUseText()
    expect(text).toContain('Stop task bfbc3f6')
  })

  it('shows different task_id', () => {
    const text = renderToolUseText({ task_id: 'abc123' })
    expect(text).toContain('Stop task abc123')
  })

  it('shows "Stop task" when task_id is missing', () => {
    const text = renderToolUseText({ task_id: undefined })
    expect(text).toContain('Stop task')
  })
})

describe('taskStop tool_result rendering', () => {
  it('shows result message content', () => {
    const text = renderToolResultText('{"message":"Successfully stopped task: bfbc3f6"}')
    expect(text).toContain('Successfully stopped task')
  })

  it('shows error message when task not found', () => {
    const text = renderToolResultText('{"message":"Task not found: xyz"}')
    expect(text).toContain('Task not found')
  })
})
