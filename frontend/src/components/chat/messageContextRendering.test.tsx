import { render, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { MESSAGE_METADATA_FIELD } from '~/generated/contracts/worker-vocab'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { diffAdded } from './diff/diffStyles.css'
import { MessageBubble } from './MessageBubble'
import './providers'
import './providers/testMocks'

function resultMessage() {
  return makeMessage({
    id: 'result',
    seq: 10n,
    spanId: 'call',
    agentProvider: AgentProvider.OPENCODE,
    content: rawContent({ sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed' }),
  })
}

describe('message context rendering', () => {
  it('keeps a TaskUpdate card on its persisted snapshot', async () => {
    const message = makeMessage({
      id: 'task-update',
      seq: 3n,
      spanId: 'task-call',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'task-call', name: 'TaskUpdate', input: { taskId: '42', status: 'completed' } }] } }),
      supplementalContent: rawContent({ metadata: { [MESSAGE_METADATA_FIELD.TodoSnapshot]: {
        id: '42',
        content: 'Persisted task title',
        status: 'TODO_STATUS_COMPLETED',
        activeForm: '',
        description: 'Persisted description',
      } } }),
      supplementalRevision: 1n,
    })
    const context = testMessageContext({ messages: () => [message] })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('Persisted task title'))
    expect(container.textContent).toContain('Persisted description')
  })

  it('loads an edit request outside the visible history window', async () => {
    const result = resultMessage()
    const request = makeMessage({
      id: 'request',
      seq: 1n,
      spanId: 'call',
      agentProvider: AgentProvider.OPENCODE,
      content: rawContent({ sessionUpdate: 'tool_call', toolCallId: 'call', kind: 'edit', rawInput: { filePath: '/project/example.ts', oldString: 'oldValue', newString: 'recoveredValue' } }),
    })
    const fetchSpan = vi.fn(async () => [request, result])
    const context = testMessageContext({ messages: () => [result], fetchSpan })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={result} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.querySelector(`.${diffAdded}`)?.textContent).toContain('recoveredValue'))
    expect(fetchSpan).toHaveBeenCalledOnce()
  })

  it('does not fetch related history for a self-contained Codex tool row', async () => {
    const message = makeMessage({
      id: 'command',
      seq: 3n,
      spanId: 'command',
      agentProvider: AgentProvider.CODEX,
      content: rawContent({ item: { type: 'commandExecution', id: 'command', status: 'completed', command: 'pwd', aggregatedOutput: '/project', exitCode: 0 } }),
    })
    const fetchSpan = vi.fn(async () => [])
    const context = testMessageContext({ messages: () => [message], fetchSpan })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('/project'))
    expect(fetchSpan).not.toHaveBeenCalled()
  })

  it('does not fetch related history during hidden measurement', () => {
    const message = resultMessage()
    const fetchSpan = vi.fn(async () => [])
    const context = testMessageContext({ messages: () => [message], fetchSpan })
    render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} premeasureMode /></PreferencesProvider>
    ))
    expect(fetchSpan).not.toHaveBeenCalled()
  })
})
