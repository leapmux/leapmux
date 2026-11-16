import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { settleWorkerReplies } from '~/test-support/fakeWorker'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { diffAdded } from './diff/diffStyles.css'
import { MessageBubble } from './MessageBubble'
import './providers'

function resultMessage() {
  return makeMessage({
    id: 'result',
    seq: 10n,
    spanId: 'call',
    agentProvider: AgentProvider.KILO,
    content: rawContent({ sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed' }),
  })
}

describe('message context rendering', () => {
  // The worker stores a TaskUpdate's post-update snapshot on the RESULT message, which it
  // reads the todo event from. The result row draws the task from it; the request row
  // never reads its result.
  it('keeps a TaskUpdate card on its persisted snapshot', async () => {
    const request = makeMessage({
      id: 'task-update',
      seq: 3n,
      spanId: 'task-call',
      spanType: 'TaskUpdate',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({
        type: 'assistant',
        message: {
          content: [
            {
              type: 'tool_use',
              id: 'task-call',
              name: 'TaskUpdate',
              input: { taskId: '42', status: 'completed' },
            },
          ],
        },
      }),
    })
    const result = makeMessage({
      id: 'task-update-result',
      seq: 4n,
      spanId: 'task-call',
      spanType: 'TaskUpdate',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({
        type: 'user',
        message: {
          content: [
            { type: 'tool_result', tool_use_id: 'task-call', content: 'Updated task #42 status' },
          ],
        },
        tool_use_result: {
          success: true,
          taskId: '42',
          updatedFields: ['status'],
        },
      }),
      supplementalContent: rawContent({
        metadata: {
          [MESSAGE_METADATA_FIELD.TodoSnapshot]: {
            id: '42',
            content: 'Persisted task title',
            status: 'TODO_STATUS_COMPLETED',
            activeForm: '',
            description: 'Persisted description',
          },
        },
      }),
      supplementalRevision: 1n,
    })
    const context = testMessageContext({
      messages: () => [request, result],
    })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={result} host={{ messages: context }} /></PreferencesProvider>
    ))
    await settleWorkerReplies()
    await waitFor(() => expect(container.textContent).toContain('Persisted task title'))
    expect(container.textContent).toContain('Persisted description')
  })

  it('loads an edit request outside the visible history window', async () => {
    const result = resultMessage()
    const request = makeMessage({
      id: 'request',
      seq: 1n,
      spanId: 'call',
      agentProvider: AgentProvider.KILO,
      content: rawContent({
        sessionUpdate: 'tool_call',
        toolCallId: 'call',
        kind: 'edit',
        rawInput: { filePath: '/project/example.ts', oldString: 'oldValue', newString: 'recoveredValue' },
      }),
    })
    const fetchSpan = vi.fn(async () => [request, result])
    const context = testMessageContext({
      messages: () => [result],
      fetchSpan,
    })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={result} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('recoveredValue'))
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  // The row draws its own header and command only while the transcript draws no request
  // row beside it, so it learns from the span whether a request row exists before it shows.
  it('fetches the span of a self-contained Codex result row whose request the window does not hold', async () => {
    const message = makeMessage({
      id: 'command',
      seq: 3n,
      spanId: 'command',
      agentProvider: AgentProvider.CODEX,
      content: rawContent({
        item: {
          type: 'commandExecution',
          id: 'command',
          status: 'completed',
          command: 'pwd',
          aggregatedOutput: '/project',
          exitCode: 0,
        },
      }),
    })
    const fetchSpan = vi.fn(async () => [])
    const context = testMessageContext({
      messages: () => [message],
      fetchSpan,
    })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('/project'))
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  it('loads an edit request outside the visible history window in a hidden measurement copy', async () => {
    // A copy that measured the result without its request reported a height that the
    // row in the list changed when the request arrived.
    const result = resultMessage()
    const request = makeMessage({
      id: 'request',
      seq: 1n,
      spanId: 'call',
      agentProvider: AgentProvider.OPENCODE,
      content: rawContent({
        sessionUpdate: 'tool_call',
        toolCallId: 'call',
        kind: 'edit',
        rawInput: { filePath: '/project/example.ts', oldString: 'oldValue', newString: 'recoveredValue' },
      }),
    })
    const fetchSpan = vi.fn(async () => [request, result])
    const context = testMessageContext({
      messages: () => [result],
      fetchSpan,
    })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={result} host={{ messages: context }} premeasureMode /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('recoveredValue'))
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  // The row draws its own header and command only while the transcript draws no request
  // row beside it, so it learns from the span whether a request row exists before it shows.
  it('fetches the span of a self-contained Codex result row whose request the window does not hold, in a hidden measurement copy', async () => {
    const message = makeMessage({
      id: 'command',
      seq: 3n,
      spanId: 'command',
      agentProvider: AgentProvider.CODEX,
      content: rawContent({
        item: {
          type: 'commandExecution',
          id: 'command',
          status: 'completed',
          command: 'pwd',
          aggregatedOutput: '/project',
          exitCode: 0,
        },
      }),
    })
    const fetchSpan = vi.fn(async () => [])
    const context = testMessageContext({
      messages: () => [message],
      fetchSpan,
    })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} premeasureMode /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('/project'))
    expect(fetchSpan).toHaveBeenCalledOnce()
  })
})
