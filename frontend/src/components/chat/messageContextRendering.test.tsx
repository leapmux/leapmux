import type { TodoItem } from '~/models/todo'
import { render, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { testMessageContext } from '~/test-support/messageContext'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { diffAdded } from './diff/diffStyles.css'
import { MessageBubble } from './MessageBubble'
import { createMessageRenderCacheStore } from './messageRenderCache'
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
  it('updates a task title through the live entity source', async () => {
    const message = makeMessage({
      id: 'task-update',
      seq: 3n,
      spanId: 'task-call',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'task-call', name: 'TaskUpdate', input: { taskId: '42', status: 'completed' } }] } }),
    })
    const [todo, setTodo] = createSignal<TodoItem>({ id: '42', rowKey: '42', content: 'Original task title', status: 'pending', activeForm: '' })
    const context = testMessageContext({ messages: () => [message], todo: id => id === '42' ? todo() : undefined })
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('Original task title'))
    await context.loadRelated(message)
    setTodo({ id: '42', rowKey: '42', content: 'Revised task title', status: 'completed', activeForm: '' })
    await waitFor(() => expect(container.textContent).toContain('Revised task title'))
    expect(container.textContent).not.toContain('Original task title')
    expect(message.supplementalRevision).toBe(0n)
  })

  // The row cache is keyed by the message revisions, and the to-do store is the one
  // extraction input that is not a message. A `TaskUpdate` states a task id and a
  // status alone, so the SUBJECT comes from the store -- and a row drawn before its
  // task arrived kept `Task #42` for the life of the tab.
  it('repairs a cached task row when its to-do reaches the store', async () => {
    const message = makeMessage({
      id: 'task-update',
      seq: 3n,
      spanId: 'task-call',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'task-call', name: 'TaskUpdate', input: { taskId: '42', status: 'completed' } }] } }),
    })
    const [todo, setTodo] = createSignal<TodoItem | undefined>(undefined)
    const context = testMessageContext({ messages: () => [message], todo: id => id === '42' ? todo() : undefined })
    const renderCache = createMessageRenderCacheStore().forRow('task-update')
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context, renderCache }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('Task #42'))
    setTodo({ id: '42', rowKey: '42', content: 'Ship the release', status: 'completed', activeForm: '' })
    await waitFor(() => expect(container.textContent).toContain('Ship the release'))
    expect(container.textContent).not.toContain('Task #42')
  })

  // The repair goes ONE way. `TodoWrite` replaces the whole list the `Task*` family
  // shares, a context clear empties it, and the cap evicts FINISHED rows first -- so a
  // task leaving the store is ordinary, and a row that was right must not go back to
  // `Task #42` when it happens.
  it('keeps a repaired task row when its to-do leaves the store', async () => {
    const message = makeMessage({
      id: 'task-update',
      seq: 3n,
      spanId: 'task-call',
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'tool_use', id: 'task-call', name: 'TaskUpdate', input: { taskId: '42', status: 'completed' } }] } }),
    })
    const [todo, setTodo] = createSignal<TodoItem | undefined>({ id: '42', rowKey: '42', content: 'Ship the release', status: 'completed', activeForm: '' })
    const context = testMessageContext({ messages: () => [message], todo: id => id === '42' ? todo() : undefined })
    const renderCache = createMessageRenderCacheStore().forRow('task-update')
    const { container } = render(() => (
      <PreferencesProvider><MessageBubble message={message} host={{ messages: context, renderCache }} /></PreferencesProvider>
    ))
    await waitFor(() => expect(container.textContent).toContain('Ship the release'))

    setTodo(undefined)
    await Promise.resolve()
    expect(container.textContent).toContain('Ship the release')
    expect(container.textContent).not.toContain('Task #42')
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
