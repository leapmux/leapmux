import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import { render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, describe, expect, it } from 'vitest'
import { ChatView } from '~/components/chat/ChatView'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'

// jsdom does not provide ResizeObserver
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

function makeMessage(role: string, text: string, id: string = '1'): AgentChatMessage {
  const content = JSON.stringify({
    message: {
      content: [{ type: 'text', text }],
    },
  })
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    role,
    content: new TextEncoder().encode(content),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
  }
}

function makeCodexCommandMessage(params: {
  id: string
  seq: bigint
  spanId: string
  status: string
  command?: string
  aggregatedOutput?: string
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'commandExecution',
        id: params.spanId,
        command: params.command ?? 'echo hi',
        cwd: '/tmp',
        status: params.status,
        aggregatedOutput: params.aggregatedOutput ?? '',
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'commandExecution',
  } as AgentChatMessage
}

function makeCodexFileChangeMessage(params: {
  id: string
  seq: bigint
  spanId: string
  status: string
  diff?: string
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'fileChange',
        id: params.spanId,
        status: params.status,
        changes: params.status === 'completed'
          ? [{ path: 'a.txt', kind: 'update', diff: params.diff ?? '@@ -1 +1 @@\n-old\n+new' }]
          : [],
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'fileChange',
  } as AgentChatMessage
}

function makeCodexReasoningMessage(params: {
  id: string
  seq: bigint
  spanId: string
  summary?: string[]
  content?: string[]
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'reasoning',
        id: params.spanId,
        summary: params.summary ?? [],
        content: params.content ?? [],
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'reasoning',
  } as AgentChatMessage
}

describe('chatView', () => {
  it('renders empty state when no messages', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeTruthy()
  })

  it('renders messages', () => {
    const messages = [
      makeMessage('user', 'Hello', '1'),
      makeMessage('assistant', 'Hi there', '2'),
    ]
    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Hello')).toBeTruthy()
    expect(screen.getByText('Hi there')).toBeTruthy()
  })

  it('renders streaming text', async () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="Thinking..." />
      </PreferencesProvider>
    ))
    // Streaming text rendering is throttled via requestAnimationFrame
    await waitFor(() => expect(screen.getByText('Thinking...')).toBeTruthy())
  })

  it('renders chat container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('chat-container')).toBeTruthy()
  })

  it('renders live command stream inside the matching codex command bubble', () => {
    const messages = [
      makeCodexCommandMessage({ id: 'cmd-start', seq: 1n, spanId: 'cmd-1', status: 'in_progress' }),
    ]
    const commandStream: CommandStreamSegment[] = [
      { kind: 'output', text: 'building...\n' },
      { kind: 'interaction', text: 'y\n' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          getCommandStreamBySpanId={() => commandStream}
        />
      </PreferencesProvider>
    ))

    expect(screen.getByText('building...')).toBeTruthy()
    expect(screen.getByText('> y')).toBeTruthy()
  })

  it('hides stale codex commandExecution start message once completed message exists for the same span', () => {
    const messages = [
      makeCodexCommandMessage({ id: 'cmd-start', seq: 1n, spanId: 'cmd-1', status: 'in_progress' }),
      makeCodexCommandMessage({ id: 'cmd-done', seq: 2n, spanId: 'cmd-1', status: 'completed', aggregatedOutput: 'done\n' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByText('echo hi')).toHaveLength(1)
    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
  })

  it('renders live fileChange stream inside the matching codex fileChange bubble', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-start', seq: 1n, spanId: 'fc-1', status: 'in_progress' }),
    ]
    const fileStream: CommandStreamSegment[] = [
      { kind: 'output', text: 'updating a.txt\n' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          getCommandStreamBySpanId={() => fileStream}
        />
      </PreferencesProvider>
    ))

    expect(screen.getByText('updating a.txt')).toBeTruthy()
  })

  it('hides stale codex fileChange start message once completed message exists for the same span', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-start', seq: 1n, spanId: 'fc-1', status: 'in_progress' }),
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 2n, spanId: 'fc-1', status: 'completed' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
  })

  it('renders live reasoning stream inside the matching codex reasoning bubble', () => {
    const messages = [
      makeCodexReasoningMessage({ id: 'reason-start', seq: 1n, spanId: 'reason-1' }),
    ]
    const reasoningStream: CommandStreamSegment[] = [
      { kind: 'reasoning_summary', text: 'first summary' },
      { kind: 'reasoning_summary_break', text: '' },
      { kind: 'reasoning_summary', text: 'second summary' },
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          getCommandStreamBySpanId={() => reasoningStream}
        />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(screen.getByText('Thinking')).toBeTruthy()
  })

  it('hides stale codex reasoning start message once completed message exists for the same span', () => {
    const messages = [
      makeCodexReasoningMessage({ id: 'reason-start', seq: 1n, spanId: 'reason-1' }),
      makeCodexReasoningMessage({ id: 'reason-done', seq: 2n, spanId: 'reason-1', summary: ['done'] }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(screen.getByText('Thinking')).toBeTruthy()
  })
})
