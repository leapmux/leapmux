import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeAll, describe, expect, it } from 'vitest'
import { ChatView } from '~/components/chat/ChatView'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'

const A_TXT_RE = /a\.txt/
const B_TXT_RE = /b\.txt/

// jsdom does not provide ResizeObserver or Worker
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
  globalThis.Worker ??= class {
    onmessage: ((e: MessageEvent) => void) | null = null
    onerror: ((e: ErrorEvent) => void) | null = null
    postMessage() {}
    terminate() {}
    addEventListener() {}
    removeEventListener() {}
    dispatchEvent() { return false }
  } as unknown as typeof Worker
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
  processId?: string
  exitCode?: number
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
        processId: params.processId,
        status: params.status,
        aggregatedOutput: params.aggregatedOutput ?? '',
        exitCode: params.exitCode,
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
  changes?: Array<Record<string, unknown>>
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
        changes: params.changes ?? (params.status === 'completed'
          ? [{ path: 'a.txt', kind: 'update', diff: params.diff ?? '@@ -1 +1 @@\n-old\n+new' }]
          : []),
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

function makeCodexTurnPlanMessage(params: {
  id: string
  seq: bigint
  explanation?: string | null
  plan: Array<{ step: string, status: string }>
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify({
      method: 'turn/plan/updated',
      params: {
        threadId: 'thread-1',
        turnId: 'turn-1',
        explanation: params.explanation ?? null,
        plan: params.plan,
      },
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
  } as AgentChatMessage
}

function makeCodexWebSearchMessage(params: {
  id: string
  seq: bigint
  spanId: string
  query?: string
  action?: Record<string, unknown>
  completed?: boolean
}): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id: params.id,
    role: MessageRole.ASSISTANT,
    content: new TextEncoder().encode(JSON.stringify({
      item: {
        type: 'webSearch',
        id: params.spanId,
        query: params.query ?? '',
        action: params.action ?? { type: 'other' },
      },
      threadId: 'thread-1',
      turnId: 'turn-1',
    })),
    contentCompression: ContentCompression.NONE,
    seq: params.seq,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
    spanId: params.spanId,
    spanType: 'webSearch',
    spanLines: params.completed ? JSON.stringify([{ span_id: params.spanId, color: 1, type: 'connector_end' }]) : '[]',
  } as AgentChatMessage
}

function makeCodexHiddenLifecycleMessage(id: string = 'codex-hidden'): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    role: MessageRole.LEAPMUX,
    content: new TextEncoder().encode(JSON.stringify({
      old_seqs: [],
      messages: [
        {
          method: 'thread/started',
          params: { threadId: 'thread-1' },
        },
      ],
    })),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    agentProvider: AgentProvider.CODEX,
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

  it('renders empty state when all messages are hidden', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeCodexHiddenLifecycleMessage()]} streamingText="" />
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

  it('preserves expanded codex reasoning state when the message updates and new messages are appended', async () => {
    const initialMessages = [
      makeCodexReasoningMessage({
        id: 'reasoning-1',
        seq: 1n,
        spanId: 'reasoning-span-1',
        summary: ['Initial reasoning summary'],
      }),
    ]
    let setMessages!: (messages: AgentChatMessage[]) => void

    render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} streamingText="" />
        </PreferencesProvider>
      )
    })

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('Initial reasoning summary')).toBeTruthy()

    setMessages([
      makeCodexReasoningMessage({
        id: 'reasoning-1',
        seq: 2n,
        spanId: 'reasoning-span-1',
        summary: ['Initial reasoning summary'],
      }),
      makeMessage('assistant', 'Follow-up message', 'assistant-2'),
    ])

    await waitFor(() => expect(screen.getByText('Initial reasoning summary')).toBeTruthy())
    expect(screen.getByText('Follow-up message')).toBeTruthy()
  })

  it('does not snap to bottom after older messages are prepended while browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({ ...message, seq: BigInt(100 + index) }))
    let setMessages!: (messages: AgentChatMessage[]) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      setMessages = updateMessages
      return (
        <PreferencesProvider>
          <ChatView messages={messages()} streamingText="" hasOlderMessages={true} />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 2000
    let clientHeight = 500
    Object.defineProperty(messageList, 'scrollTop', {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = value
      },
    })
    Object.defineProperty(messageList, 'scrollHeight', {
      configurable: true,
      get: () => scrollHeight,
    })
    Object.defineProperty(messageList, 'clientHeight', {
      configurable: true,
      get: () => clientHeight,
    })

    setMessages([...initialMessages])
    await new Promise(resolve => requestAnimationFrame(() => resolve(undefined)))

    scrollTop = 50
    fireEvent.scroll(messageList)

    scrollHeight = 2600
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
    ])

    await waitFor(() => expect(scrollTop).toBe(650))

    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container.textContent).toContain('Newest 3'))
    expect(scrollTop).toBe(650)
  })

  it('keeps both codex commandExecution start and completed messages in history', () => {
    const messages = [
      makeCodexCommandMessage({ id: 'cmd-start', seq: 1n, spanId: 'cmd-1', status: 'in_progress' }),
      makeCodexCommandMessage({ id: 'cmd-done', seq: 2n, spanId: 'cmd-1', status: 'completed', aggregatedOutput: 'done\n' }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    expect(screen.getByText('done')).toBeTruthy()
    expect(view.container.textContent).toContain('echo hi')
  })

  it('renders long completed codex command output with the shared collapsed result UI', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-done',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'completed',
        aggregatedOutput: 'line1\nline2\nline3\nline4\n',
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('message-toolbar')).toBeTruthy()
    expect(view.container.textContent).toContain('line1')
    expect(view.container.textContent).toContain('line3')
    expect(screen.queryByText('line4')).toBeNull()
  })

  it('strips tool use header DOM from completed codex command output', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-done',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'failed',
        aggregatedOutput: [
          'TestingLibraryElementError',
          '  <div',
          '    class="toolStyles_toolUseHeader__174i4tc1"',
          '  >',
          '    <span>0 files</span>',
          '  </div>',
          'real failure output',
        ].join('\n'),
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('real failure output')
    expect(screen.queryByText('0 files')).toBeNull()
  })

  it('renders process ID and exit code in completed codex command failures without output', () => {
    const messages = [
      makeCodexCommandMessage({
        id: 'cmd-failed',
        seq: 1n,
        spanId: 'cmd-1',
        status: 'failed',
        aggregatedOutput: '',
        processId: '63628',
        exitCode: 1,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('Error (exit code: 1)')
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

  it('keeps both codex fileChange start and completed messages in history', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-start', seq: 1n, spanId: 'fc-1', status: 'in_progress' }),
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 2n, spanId: 'fc-1', status: 'completed' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(2)
    expect(screen.getByText('0 files')).toBeTruthy()
    expect(screen.getByText('old')).toBeTruthy()
  })

  it('does not render a diff in the codex fileChange start message', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: 'a.txt', kind: 'update', diff: '@@ -1 +1 @@\n-old\n+new' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('a.txt')
    expect(view.container.textContent).not.toContain('-old')
    expect(view.container.textContent).not.toContain('+new')
  })

  it('renders a simple codex fileChange with Edit-style diff content', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 1n, spanId: 'fc-1', status: 'completed' }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.queryByText('completed')).toBeNull()
    expect(screen.getByText('old')).toBeTruthy()
    expect(screen.getByText('new')).toBeTruthy()
    expect(screen.queryByText(A_TXT_RE)).toBeNull()
    expect(screen.queryByTestId('git-diff-stats')).toBeNull()
  })

  it('renders a simple codex add fileChange start message like Write tool_use', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: '/repo/src/new-file.ts', kind: { type: 'add' }, diff: 'export const hello = "world"\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('new-file.ts')
    expect(view.container.textContent).not.toContain('export const hello = "world"')
  })

  it('renders a simple codex add fileChange completion like Write tool_use_result', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [{ path: '/repo/src/new-file.ts', kind: { type: 'add' }, diff: 'export const hello = "world"\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('export const hello = "world"')
    expect(screen.getByTestId('message-toolbar')).toBeTruthy()
  })

  it('renders multi-file codex fileChange entries with per-file labels including adds', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          { path: '/repo/src/a.ts', kind: { type: 'update' }, diff: '@@ -1 +1 @@\n-oldValue\n+newValue\n' },
          { path: '/repo/src/new-file.tsx', kind: { type: 'add' }, diff: 'export const hello = "world"\n' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('a.ts +1 -1')
    expect(view.container.textContent).toContain('new-file.tsx +1')
    expect(view.container.textContent).toContain('+1')
    expect(view.container.textContent).toContain('oldValue')
    expect(view.container.textContent).toContain('newValue')
    expect(view.container.textContent).toContain('export const hello = "world"')
  })

  it('shows shared toolbar actions for completed codex fileChange messages', () => {
    const messages = [
      makeCodexFileChangeMessage({ id: 'fc-done', seq: 1n, spanId: 'fc-1', status: 'completed' }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    const toolbar = screen.getByTestId('message-toolbar')
    expect(toolbar).toBeTruthy()
    expect(screen.getByTestId('message-copy-json')).toBeTruthy()
    expect(view.container.querySelectorAll('[data-testid="message-toolbar"] button').length).toBeGreaterThanOrEqual(2)
  })

  it('falls back to codex fileChange summary for mixed changes', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [
          { path: 'a.txt', kind: 'create', diff: '' },
          { path: 'b.txt', kind: 'delete', diff: '' },
        ],
      }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByText('2 files changed')).toBeTruthy()
    expect(screen.queryByText(A_TXT_RE)).toBeNull()
    expect(screen.queryByText(B_TXT_RE)).toBeNull()
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

  it('keeps completed codex reasoning visible while empty start reasoning remains hidden', () => {
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

  it('renders turn/plan/updated with the TodoWrite-style todo list UI', () => {
    const messages = [
      makeCodexTurnPlanMessage({
        id: 'plan-1',
        seq: 1n,
        plan: [
          { step: 'Inspect message filtering', status: 'inProgress' },
          { step: 'Implement renderer', status: 'pending' },
          { step: 'Run tests', status: 'completed' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('3 tasks')
    expect(view.container.textContent).toContain('Inspect message filtering')
    expect(view.container.textContent).toContain('Implement renderer')
    expect(view.container.textContent).toContain('Run tests')
  })

  it('renders turn/plan/updated explanation in the title when present', () => {
    const messages = [
      makeCodexTurnPlanMessage({
        id: 'plan-1',
        seq: 1n,
        explanation: 'Need to keep start and complete messages visible',
        plan: [
          { step: 'Inspect message filtering', status: 'inProgress' },
          { step: 'Implement renderer', status: 'pending' },
        ],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('2 tasks - Need to keep start and complete messages visible')
  })

  it('renders codex webSearch start and completed search messages separately', () => {
    const messages = [
      makeCodexWebSearchMessage({ id: 'ws-start', seq: 1n, spanId: 'ws-1' }),
      makeCodexWebSearchMessage({
        id: 'ws-done',
        seq: 2n,
        spanId: 'ws-1',
        query: 'codex app server',
        action: {
          type: 'search',
          query: 'codex app server',
          queries: [
            'codex app server',
            'site:github.com openai codex "turn/plan/updated"',
            'site:github.com "turn/plan/updated" codex app server',
          ],
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getAllByTestId('message-bubble')).toHaveLength(1)
    expect(view.container.textContent).not.toContain('Searching the web')
    expect(view.container.textContent).toContain('codex app server')
    expect(view.container.textContent).not.toContain('site:github.com openai codex')
    expect(view.container.textContent).not.toContain('site:github.com "turn/plan/updated" codex app server')

    fireEvent.click(screen.getByRole('button', { name: 'Expand' }))

    expect(view.container.textContent).toContain('site:github.com openai codex "turn/plan/updated"')
    expect(view.container.textContent).toContain('site:github.com "turn/plan/updated" codex app server')
  })

  it('renders codex webSearch openPage messages like a fetch/opened-page result', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-open',
        seq: 1n,
        spanId: 'ws-1',
        query: 'https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md',
        action: {
          type: 'openPage',
          url: 'https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md',
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md')
    expect(screen.getByTestId('message-toolbar')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Expand' })).toBeNull()
  })

  it('renders codex webSearch findInPage messages with pattern and URL', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-find',
        seq: 1n,
        spanId: 'ws-1',
        query: 'README',
        action: {
          type: 'findInPage',
          url: 'https://example.com/readme',
          pattern: 'turn/plan/updated',
        },
        completed: true,
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container.textContent).toContain('turn/plan/updated')
    expect(view.container.textContent).toContain('https://example.com/readme')
    expect(screen.getByTestId('message-toolbar')).toBeTruthy()
  })

  it('hides empty completed codex webSearch messages with no query or detail', () => {
    const messages = [
      makeCodexWebSearchMessage({
        id: 'ws-empty',
        seq: 1n,
        spanId: 'ws-1',
        query: '',
        action: { type: 'other' },
        completed: true,
      }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.queryByText('Searched')).toBeNull()
    expect(screen.queryByText('Searching the web')).toBeNull()
    expect(screen.queryByTestId('message-bubble')).toBeNull()
  })
})
