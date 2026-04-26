import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { CommandStreamSegment } from '~/stores/chat.store'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { ChatView } from '~/components/chat/ChatView'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { AgentProvider, AgentStatus, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'

const A_TXT_RE = /a\.txt/
const B_TXT_RE = /b\.txt/
const resizeObserverCallbacks: ResizeObserverCallback[] = []

// jsdom does not provide ResizeObserver or Worker
beforeAll(() => {
  globalThis.ResizeObserver = class {
    private callback: ResizeObserverCallback

    constructor(callback: ResizeObserverCallback) {
      this.callback = callback
      resizeObserverCallbacks.push(callback)
    }

    observe() {}

    unobserve() {}

    disconnect() {
      const idx = resizeObserverCallbacks.indexOf(this.callback)
      if (idx >= 0)
        resizeObserverCallbacks.splice(idx, 1)
    }
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

async function flushAnimationFrame() {
  await new Promise(resolve => requestAnimationFrame(() => resolve(undefined)))
}

async function triggerResizeObservers() {
  for (const callback of [...resizeObserverCallbacks])
    callback([], {} as ResizeObserver)
  await flushAnimationFrame()
}

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

function makeClaudeEnterPlanModeResultMessage(id: string = 'claude-enter-plan-result'): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage',
    id,
    role: MessageRole.USER,
    content: new TextEncoder().encode(JSON.stringify({
      type: 'user',
      message: {
        role: 'user',
        content: [
          {
            type: 'tool_result',
            content: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
            tool_use_id: 'toolu_01U3MQbUE7bmTs1SnJx4SPU3',
          },
        ],
      },
      tool_use_result: {
        message: 'Entered plan mode. You should now focus on exploring the codebase and designing an implementation approach.',
      },
    })),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    createdAt: '',
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId: 'toolu_01U3MQbUE7bmTs1SnJx4SPU3',
    spanType: 'EnterPlanMode',
  } as AgentChatMessage
}

describe('chatView', () => {
  it('renders empty state when no messages', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
  })

  // The AgentStartupBanner sub-component is the visible surface of the
  // backend's phased STARTING broadcasts. These tests lock in the
  // fallback → phase-message → error contract so a regression in the
  // startup-panel plumbing is caught at the unit level.
  it('shows the default "Starting <provider>…" label when no startup_message is set', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentStatus={AgentStatus.STARTING}
          providerLabel="Claude Code"
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('agent-startup-overlay')).toBeInTheDocument()
    expect(screen.getByText('Starting Claude Code…')).toBeInTheDocument()
  })

  it('shows the backend startup_message when one is provided (overrides the default)', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentStatus={AgentStatus.STARTING}
          providerLabel="Claude Code"
          startupMessage="Checking Git status…"
        />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Checking Git status…')).toBeInTheDocument()
    // Default label must not also render — the backend message wins.
    expect(screen.queryByText('Starting Claude Code…')).not.toBeInTheDocument()
  })

  it('falls back to the default label when startup_message is empty string', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentStatus={AgentStatus.STARTING}
          providerLabel="Claude Code"
          startupMessage=""
        />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Starting Claude Code…')).toBeInTheDocument()
  })

  it('renders the startup-error banner with the server error on STARTUP_FAILED', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={[]}
          streamingText=""
          agentStatus={AgentStatus.STARTUP_FAILED}
          providerLabel="Claude Code"
          startupError="exec: claude: not found"
        />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('agent-startup-error')).toBeInTheDocument()
    expect(screen.getByText('Claude Code failed to start')).toBeInTheDocument()
    expect(screen.getByText('exec: claude: not found')).toBeInTheDocument()
  })

  it('renders empty state when all messages are hidden', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeCodexHiddenLifecycleMessage()]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
  })

  it('hides EnterPlanMode tool_result messages in chat history', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[makeClaudeEnterPlanModeResultMessage()]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByText('Send a message to start')).toBeInTheDocument()
    expect(screen.queryByText('Entered plan mode')).not.toBeInTheDocument()
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
    expect(screen.getByText('Hello')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })

  it('renders streaming text', async () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="Thinking..." />
      </PreferencesProvider>
    ))
    // Streaming text rendering is throttled via requestAnimationFrame
    await waitFor(() => expect(screen.getByText('Thinking...')).toBeInTheDocument())
  })

  it('renders chat container', () => {
    render(() => (
      <PreferencesProvider>
        <ChatView messages={[]} streamingText="" />
      </PreferencesProvider>
    ))
    expect(screen.getByTestId('chat-container')).toBeInTheDocument()
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

    expect(screen.getByText('building...')).toBeInTheDocument()
    expect(screen.getByText('> y')).toBeInTheDocument()
  })

  it('preserves expanded codex reasoning state when the message updates and new messages are appended', async () => {
    localStorage.setItem('leapmux:browser-prefs', JSON.stringify({ expandAgentThoughts: false }))

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

    expect(screen.queryByText('Initial reasoning summary')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('Initial reasoning summary')).toBeInTheDocument()

    setMessages([
      makeCodexReasoningMessage({
        id: 'reasoning-1',
        seq: 2n,
        spanId: 'reasoning-span-1',
        summary: ['Initial reasoning summary'],
      }),
      makeMessage('assistant', 'Follow-up message', 'assistant-2'),
    ])

    await waitFor(() => expect(screen.getByText('Initial reasoning summary')).toBeInTheDocument())
    expect(screen.getByText('Follow-up message')).toBeInTheDocument()
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
    const clientHeight = 500
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
    await flushAnimationFrame()

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

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    expect(scrollTop).toBe(650)
  })

  it('does not snap to bottom when a new message arrives before older-message anchoring finishes', async () => {
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
    const clientHeight = 500
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
    await flushAnimationFrame()

    scrollTop = 50
    fireEvent.scroll(messageList)

    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    await waitFor(() => expect(scrollTop).toBe(750))
  })

  it('does not snap to bottom after older loading finishes while the user is still browsing history', async () => {
    const initialMessages = [
      makeMessage('assistant', 'Newest 1', 'msg-100'),
      makeMessage('assistant', 'Newest 2', 'msg-101'),
    ].map((message, index) => ({ ...message, seq: BigInt(100 + index) }))
    let setMessages!: (messages: AgentChatMessage[]) => void
    let setFetchingOlder!: (value: boolean) => void

    const view = render(() => {
      const [messages, updateMessages] = createSignal(initialMessages)
      const [fetchingOlder, updateFetchingOlder] = createSignal(false)
      setMessages = updateMessages
      setFetchingOlder = updateFetchingOlder
      return (
        <PreferencesProvider>
          <ChatView
            messages={messages()}
            streamingText=""
            hasOlderMessages={true}
            fetchingOlder={fetchingOlder()}
          />
        </PreferencesProvider>
      )
    })

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    let scrollHeight = 2000
    const clientHeight = 500
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
    await flushAnimationFrame()

    scrollTop = 50
    fireEvent.scroll(messageList)
    setFetchingOlder(true)

    scrollHeight = 2600
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
    ])

    await waitFor(() => expect(scrollTop).toBe(650))

    setFetchingOlder(false)
    scrollHeight = 2700
    setMessages([
      { ...makeMessage('assistant', 'Older 1', 'msg-050'), seq: 50n },
      { ...makeMessage('assistant', 'Older 2', 'msg-051'), seq: 51n },
      ...initialMessages,
      { ...makeMessage('assistant', 'Newest 3', 'msg-102'), seq: 102n },
    ])

    await waitFor(() => expect(view.container).toHaveTextContent('Newest 3'))
    expect(scrollTop).toBe(650)
  })

  it('suppresses passive older-message loading when restored to top after trim clamping', async () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()
    const onClearSavedViewportScroll = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          hasOlderMessages={true}
          savedViewportScroll={{ distFromBottom: 9999, atBottom: false }}
          onLoadOlderMessages={onLoadOlderMessages}
          onClearSavedViewportScroll={onClearSavedViewportScroll}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement

    let scrollTop = 0
    const scrollHeight = 300
    let clientHeight = 0
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

    clientHeight = 100
    await triggerResizeObservers()

    expect(scrollTop).toBe(0)
    expect(onClearSavedViewportScroll).toHaveBeenCalledTimes(1)

    fireEvent.scroll(messageList)
    expect(onLoadOlderMessages).not.toHaveBeenCalled()
  })

  it('loads older messages on explicit upward wheel intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          hasOlderMessages={true}
          onLoadOlderMessages={onLoadOlderMessages}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    fireEvent.wheel(messageList, { deltaY: -20 })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('loads older messages on explicit upward keyboard intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          hasOlderMessages={true}
          onLoadOlderMessages={onLoadOlderMessages}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    fireEvent.keyDown(messageList, { key: 'PageUp' })
    expect(onLoadOlderMessages).toHaveBeenCalledTimes(1)
  })

  it('scrolls the active chat by one page programmatically', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    let pageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          pageScrollRef={(fn) => { pageScroll = fn }}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    messageList.scrollBy = vi.fn()
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 240 })

    pageScroll(1)
    expect(messageList.scrollBy).toHaveBeenCalledWith({ top: 240, behavior: 'auto' })
  })

  it('scrolls only the targeted chat when multiple chat views are mounted', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    let hiddenPageScroll!: (direction: -1 | 1) => void
    let visiblePageScroll!: (direction: -1 | 1) => void

    render(() => (
      <PreferencesProvider>
        <div>
          <div style={{ display: 'none' }}>
            <ChatView
              messages={messages}
              streamingText=""
              pageScrollRef={(fn) => { hiddenPageScroll = fn }}
            />
          </div>
          <div>
            <ChatView
              messages={messages}
              streamingText=""
              pageScrollRef={(fn) => { visiblePageScroll = fn }}
            />
          </div>
        </div>
      </PreferencesProvider>
    ))

    const chatContainers = screen.getAllByTestId('chat-container')
    const hiddenList = chatContainers[0].firstElementChild?.firstElementChild as HTMLDivElement
    const visibleList = chatContainers[1].firstElementChild?.firstElementChild as HTMLDivElement

    Object.defineProperty(hiddenList, 'clientHeight', { configurable: true, get: () => 120 })
    Object.defineProperty(visibleList, 'clientHeight', { configurable: true, get: () => 240 })
    hiddenList.scrollBy = vi.fn()
    visibleList.scrollBy = vi.fn()

    visiblePageScroll(-1)

    expect(hiddenList.scrollBy).not.toHaveBeenCalled()
    expect(visibleList.scrollBy).toHaveBeenCalledWith({ top: -240, behavior: 'auto' })

    hiddenPageScroll(1)

    expect(hiddenList.scrollBy).toHaveBeenCalledWith({ top: 120, behavior: 'auto' })
  })

  it('loads older messages on touch and pointer overscroll intent while at top', () => {
    const messages = [
      makeMessage('assistant', 'Retained 1', 'msg-1'),
      makeMessage('assistant', 'Retained 2', 'msg-2'),
    ].map((message, index) => ({ ...message, seq: BigInt(index + 1) }))
    const onLoadOlderMessages = vi.fn()

    render(() => (
      <PreferencesProvider>
        <ChatView
          messages={messages}
          streamingText=""
          hasOlderMessages={true}
          onLoadOlderMessages={onLoadOlderMessages}
        />
      </PreferencesProvider>
    ))

    const chatContainer = screen.getByTestId('chat-container')
    const messageList = chatContainer.firstElementChild?.firstElementChild as HTMLDivElement
    Object.defineProperty(messageList, 'scrollTop', { configurable: true, get: () => 0 })
    Object.defineProperty(messageList, 'clientHeight', { configurable: true, get: () => 200 })

    fireEvent.touchStart(messageList, { touches: [{ clientY: 100 }] })
    fireEvent.touchMove(messageList, { touches: [{ clientY: 120 }] })
    fireEvent.pointerDown(messageList, { pointerType: 'touch', clientY: 100 })
    fireEvent.pointerMove(messageList, { pointerType: 'touch', clientY: 120 })

    expect(onLoadOlderMessages).toHaveBeenCalledTimes(2)
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
    expect(screen.getByText('done')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('echo hi')
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

    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(view.container).toHaveTextContent('line1')
    expect(view.container).toHaveTextContent('line3')
    expect(screen.queryByText('line4')).not.toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('real failure output')
    expect(screen.queryByText('0 files')).not.toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('Error (exit 1)')
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

    expect(screen.getByText('updating a.txt')).toBeInTheDocument()
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
    expect(screen.getByText('0 files')).toBeInTheDocument()
    expect(screen.getByText('old')).toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('a.txt')
    expect(view.container).not.toHaveTextContent('-old')
    expect(view.container).not.toHaveTextContent('+new')
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

    expect(screen.queryByText('completed')).not.toBeInTheDocument()
    expect(screen.getByText('old')).toBeInTheDocument()
    expect(screen.getByText('new')).toBeInTheDocument()
    expect(screen.queryByText(A_TXT_RE)).not.toBeInTheDocument()
    expect(screen.queryByTestId('git-diff-stats')).not.toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('new-file.ts')
    expect(view.container).not.toHaveTextContent('export const hello = "world"')
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

    expect(view.container).toHaveTextContent('export const hello = "world"')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
  })

  it('renders a simple codex delete fileChange start message as file deletion', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-start',
        seq: 1n,
        spanId: 'fc-1',
        status: 'in_progress',
        changes: [{ path: '/repo/src/old-file.ts', kind: { type: 'delete' }, diff: 'export const old = true\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('old-file.ts')
    expect(view.container).not.toHaveTextContent('export const old = true')
  })

  it('renders a simple codex delete fileChange completion as file deletion', () => {
    const messages = [
      makeCodexFileChangeMessage({
        id: 'fc-done',
        seq: 1n,
        spanId: 'fc-1',
        status: 'completed',
        changes: [{ path: '/repo/src/old-file.ts', kind: { type: 'delete' }, diff: 'export const old = true\n' }],
      }),
    ]

    const view = render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(view.container).toHaveTextContent('Deleted /repo/src/old-file.ts')
    expect(view.container).not.toHaveTextContent('1 file changed')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('a.ts +1 -1')
    expect(view.container).toHaveTextContent('new-file.tsx +1')
    expect(view.container).toHaveTextContent('+1')
    expect(view.container).toHaveTextContent('oldValue')
    expect(view.container).toHaveTextContent('newValue')
    expect(view.container).toHaveTextContent('export const hello = "world"')
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
    expect(toolbar).toBeInTheDocument()
    expect(screen.getByTestId('message-copy-json')).toBeInTheDocument()
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

    expect(screen.getByText('2 files changed')).toBeInTheDocument()
    expect(screen.queryByText(A_TXT_RE)).not.toBeInTheDocument()
    expect(screen.queryByText(B_TXT_RE)).not.toBeInTheDocument()
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
    expect(screen.getByText('Thinking')).toBeInTheDocument()
  })

  it('starts codex reasoning collapsed when expandAgentThoughts is disabled', () => {
    localStorage.setItem('leapmux:browser-prefs', JSON.stringify({ expandAgentThoughts: false }))

    const messages = [
      makeCodexReasoningMessage({ id: 'reason-done', seq: 1n, spanId: 'reason-1', summary: ['done'] }),
    ]

    render(() => (
      <PreferencesProvider>
        <ChatView messages={messages} streamingText="" />
      </PreferencesProvider>
    ))

    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.queryByText('done')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('done')).toBeInTheDocument()
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
    expect(screen.getByText('Thinking')).toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('3 tasks')
    expect(view.container).toHaveTextContent('Inspect message filtering')
    expect(view.container).toHaveTextContent('Implement renderer')
    expect(view.container).toHaveTextContent('Run tests')
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

    expect(view.container).toHaveTextContent('2 tasks - Need to keep start and complete messages visible')
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
    expect(view.container).not.toHaveTextContent('Searching the web')
    expect(view.container).toHaveTextContent('codex app server')
    expect(view.container).not.toHaveTextContent('site:github.com openai codex')
    expect(view.container).not.toHaveTextContent('site:github.com "turn/plan/updated" codex app server')

    fireEvent.click(screen.getByRole('button', { name: 'Expand' }))

    expect(view.container).toHaveTextContent('site:github.com openai codex "turn/plan/updated"')
    expect(view.container).toHaveTextContent('site:github.com "turn/plan/updated" codex app server')
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

    expect(view.container).toHaveTextContent('https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Expand' })).not.toBeInTheDocument()
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

    expect(view.container).toHaveTextContent('turn/plan/updated')
    expect(view.container).toHaveTextContent('https://example.com/readme')
    expect(screen.getByTestId('message-toolbar')).toBeInTheDocument()
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

    expect(screen.queryByText('Searched')).not.toBeInTheDocument()
    expect(screen.queryByText('Searching the web')).not.toBeInTheDocument()
    expect(screen.queryByTestId('message-bubble')).not.toBeInTheDocument()
  })
})
