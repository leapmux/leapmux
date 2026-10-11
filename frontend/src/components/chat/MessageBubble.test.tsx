import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { codeCopyHostClass } from '~/components/chat/markdownEditor/markdownContent.css'
import { MessageBubble } from '~/components/chat/MessageBubble'
import { classifyAgentMessage } from '~/components/chat/messageClassifier'
import { MessageContextMenuHostProvider } from '~/components/chat/MessageContextMenuHost'
import { messageRowChromeClass } from '~/components/chat/messageRowLayout'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { toolBodyContent, toolHeaderTimestamp } from '~/components/chat/toolStyles.css'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { KEY_BROWSER_PREFS, localStorageSet } from '~/lib/browserStorage'
import { makeMessage, rawContent, wrapContent } from '~/test-support/messageFactory'
import { toolFrame } from '~/test-support/mimoFixtures'

// jsdom does not provide ResizeObserver or Worker.
beforeAll(() => {
  if (!globalThis.ResizeObserver) {
    Object.defineProperty(globalThis, 'ResizeObserver', {
      configurable: true,
      writable: true,
      value: class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    })
  }
  if (!globalThis.Worker) {
    Object.defineProperty(globalThis, 'Worker', {
      configurable: true,
      writable: true,
      value: class {
        onmessage: ((e: MessageEvent) => void) | null = null
        onerror: ((e: ErrorEvent) => void) | null = null
        postMessage() {}
        terminate() {}
        addEventListener() {}
        removeEventListener() {}
        dispatchEvent() { return false }
      },
    })
  }
})

// Track clipboard writes for assertions.
let clipboardContent: string | null = null

beforeEach(() => {
  clipboardContent = null
  Object.assign(navigator, {
    clipboard: {
      writeText: vi.fn((text: string) => {
        clipboardContent = text
        return Promise.resolve()
      }),
    },
  })
})

function makeMsg(overrides: Partial<Parameters<typeof makeMessage>[0]>) {
  return makeMessage({ createdAt: '2025-01-15T10:00:00.000Z', ...overrides })
}

describe('standalone MCP result actions', () => {
  const output = Array.from({ length: 8 }, (_, index) => `Line ${index}`).join('\n')
  it.each([
    { provider: AgentProvider.ZCODE, content: { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'call', result: { success: true, content: output, display: { kind: 'mcp_tool', serverName: 'docs', toolName: 'lookup' } } } } },
    { provider: AgentProvider.CODEX, content: { item: { id: 'call', type: 'mcpToolCall', status: 'completed', server: 'docs', tool: 'lookup', arguments: {}, result: { content: [{ type: 'text', text: output }] } } } },
    { provider: AgentProvider.PI, content: { type: 'tool_execution_end', toolCallId: 'call', toolName: 'mcp', result: { content: [{ type: 'text', text: output }], details: { server: 'docs', tool: 'lookup' } } } },
    { provider: AgentProvider.MIMO_CODE, content: toolFrame('docs_lookup', { input: {}, output }, 'call') },
  ])('renders one result toolbar for provider $provider', ({ provider, content }) => {
    render(() => <PreferencesProvider><MessageBubble message={makeMsg({ agentProvider: provider, source: MessageSource.AGENT, spanId: 'call', content: rawContent(content) })} /></PreferencesProvider>)
    expect(screen.getAllByTestId('message-toolbar')).toHaveLength(1)
    expect(screen.getAllByRole('button', { name: 'Expand', hidden: true })).toHaveLength(1)
    expect(screen.getAllByTestId('message-copy-json')).toHaveLength(1)
  })
})

describe('tool call bubble identity', () => {
  it('keeps one call ID on its request and result without marking a text row', () => {
    const request = makeMsg({
      agentProvider: AgentProvider.CLAUDE_CODE,
      source: MessageSource.AGENT,
      seq: 1n,
      spanId: 'toolu_image',
      spanType: 'Read',
      content: rawContent({
        type: 'assistant',
        message: { content: [{ type: 'tool_use', id: 'toolu_image', name: 'Read', input: { file_path: 'image.png' } }] },
      }),
    })
    const result = makeMsg({
      agentProvider: AgentProvider.CLAUDE_CODE,
      source: MessageSource.AGENT,
      seq: 2n,
      spanId: 'toolu_image',
      spanType: 'Read',
      content: rawContent({
        type: 'user',
        message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_image', content: 'Image opened.' }] },
      }),
    })
    const text = makeMsg({
      agentProvider: AgentProvider.CLAUDE_CODE,
      source: MessageSource.AGENT,
      seq: 3n,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: 'Done.' }] } }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={request} />
        <MessageBubble message={result} />
        <MessageBubble message={text} />
      </PreferencesProvider>
    ))

    const bubbles = screen.getAllByTestId('message-bubble')
    expect(bubbles[0]).toHaveAttribute('data-tool-call-id', 'toolu_image')
    expect(bubbles[0]).toHaveAttribute('data-tool-row-role', 'request')
    expect(bubbles[0]).toHaveAttribute('data-tool-status')
    expect(bubbles[0]).toHaveAttribute('data-message-seq', '1')
    expect(bubbles[1]).toHaveAttribute('data-tool-call-id', 'toolu_image')
    expect(bubbles[1]).toHaveAttribute('data-tool-row-role', 'result')
    expect(bubbles[1]).toHaveAttribute('data-tool-status', 'completed')
    expect(bubbles[1]).toHaveAttribute('data-message-seq', '2')
    expect(bubbles[2]).not.toHaveAttribute('data-tool-call-id')
    expect(bubbles[2]).not.toHaveAttribute('data-tool-status')
    expect(bubbles[2]).not.toHaveAttribute('data-message-seq')
  })
})

/** Click the "Copy Raw JSON" button and return the parsed clipboard content. */
async function copyRawJson(): Promise<Record<string, unknown>> {
  const btn = screen.getByTestId('message-copy-json')
  fireEvent.click(btn)
  await waitFor(() => expect(clipboardContent).not.toBeNull())
  return JSON.parse(clipboardContent!)
}

interface ControlledCopyCallbacks {
  runIdle: () => number
  deliverMutations: (target: Node) => number
}

/** Run real mutation handlers and idle callbacks when the test explicitly requests them. */
async function withControlledCopyCallbacks(run: (callbacks: ControlledCopyCallbacks) => Promise<void>): Promise<void> {
  const idleCallbacks = new Map<number, IdleRequestCallback>()
  let nextIdleId = 0
  vi.stubGlobal('requestIdleCallback', (callback: IdleRequestCallback) => {
    const id = ++nextIdleId
    idleCallbacks.set(id, callback)
    return id
  })
  vi.stubGlobal('cancelIdleCallback', (id: number) => idleCallbacks.delete(id))
  const NativeMutationObserver = globalThis.MutationObserver
  const observers: ControlledMutationObserver[] = []
  class ControlledMutationObserver extends NativeMutationObserver {
    readonly targets = new Set<Node>()
    readonly records: MutationRecord[]
    readonly callback: MutationCallback

    constructor(callback: MutationCallback) {
      const records: MutationRecord[] = []
      super(batch => records.push(...batch))
      this.records = records
      this.callback = callback
      observers.push(this)
    }

    override observe(target: Node, options?: MutationObserverInit): void {
      this.targets.add(target)
      super.observe(target, options)
    }
  }
  vi.stubGlobal('MutationObserver', ControlledMutationObserver)
  try {
    await run({
      runIdle: () => {
        const pending = [...idleCallbacks.values()]
        idleCallbacks.clear()
        for (const callback of pending)
          callback({ didTimeout: false, timeRemaining: () => 50 })
        return pending.length
      },
      deliverMutations: (target) => {
        let delivered = 0
        for (const observer of observers) {
          if (!observer.targets.has(target))
            continue
          const records = [...observer.records.splice(0), ...observer.takeRecords()]
          if (records.length === 0)
            continue
          delivered += records.length
          observer.callback(records, observer)
        }
        return delivered
      },
    })
  }
  finally {
    cleanup()
    vi.unstubAllGlobals()
  }
}

// ---------------------------------------------------------------------------
// Helper: build AskUserQuestion thread messages
// ---------------------------------------------------------------------------

function askUserQuestionToolUse(questions: Array<{ header: string }>) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_ask_1',
        name: 'AskUserQuestion',
        input: {
          questions: questions.map(q => ({
            question: `Question about ${q.header}?`,
            header: q.header,
            multiSelect: false,
            options: [
              { label: 'Option A', description: 'First option' },
              { label: 'Option B', description: 'Second option' },
            ],
          })),
        },
      }],
    },
  }
}

// ---------------------------------------------------------------------------
// AskUserQuestion thread rendering
// ---------------------------------------------------------------------------

describe('askUserQuestion thread rendering', () => {
  it('shows question text for single-question tool_use', () => {
    const parent = askUserQuestionToolUse([{ header: 'Uncommitted' }])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Question about Uncommitted?')
    expect(bubble).toHaveTextContent('Option A')
    expect(bubble).toHaveTextContent('Option B')
  })

  it('shows question count for multi-question tool_use', () => {
    const parent = askUserQuestionToolUse([{ header: 'Auth' }, { header: 'Database' }])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // The row starts with the first question's header. The body includes both questions.
    expect(bubble).toHaveTextContent('Auth')
    expect(bubble).toHaveTextContent('Question about Auth?')
    expect(bubble).toHaveTextContent('Database')
  })
})

// ---------------------------------------------------------------------------
// result_divider dispatch
// ---------------------------------------------------------------------------

describe('result_divider dispatch', () => {
  it('renders a turn-end divider for a registered provider result', () => {
    // Happy path: a CLAUDE_CODE result classifies as result_divider and renders
    // through the shared row extraction as the turn-end row.
    const msg = makeMsg({
      source: MessageSource.AGENT,
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({ type: 'result', is_error: false, subtype: 'success', result: 'done', duration_ms: 1095 }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Turn ended (1.1s)')
    expect(bubble).not.toHaveTextContent('duration_ms')
  })

  it('marks a result-divider error <pre> (non-markdown) so its copy button positions top-right', async () => {
    // The result error uses a pre element outside markdownContent. The code-copy-host class
    // positions its injected Copy button at the top right.

    const msg = makeMsg({
      source: MessageSource.AGENT,
      agentProvider: AgentProvider.CLAUDE_CODE,
      content: rawContent({
        type: 'result',
        is_error: true,
        subtype: 'error_during_execution',
        result: '[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=tool_use',
        duration_ms: 261000,
      }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const content = screen.getByTestId('message-content')
    expect(content).toHaveTextContent('Error during execution')
    const pre = content.querySelector('pre')
    expect(pre).toBeInTheDocument()
    expect(pre).toHaveTextContent('ede_diagnostic')

    await waitFor(() => {
      expect(pre!.querySelector('.copy-code-button')).toBeInTheDocument()
      expect(pre!.classList.contains(codeCopyHostClass)).toBe(true)
    })
  })
})

// ---------------------------------------------------------------------------
// Unsupported provider display
// ---------------------------------------------------------------------------

describe('unsupported_provider rendering', () => {
  it('surfaces a loud error (not a guessed Claude render) for an UNSPECIFIED-provider message', () => {
    // An unknown provider displays the unsupported-provider error and Raw JSON. A result-shaped
    // payload must not become a Claude turn divider.

    const msg = makeMsg({
      source: MessageSource.AGENT,
      agentProvider: AgentProvider.UNSPECIFIED,
      content: rawContent({ type: 'result', is_error: false, subtype: 'success', result: 'done', duration_ms: 1095 }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // agentProviderLabel supplies Unknown for the zero provider enum. The banner includes that
    // label and the numeric value.

    expect(bubble).toHaveTextContent('Unsupported agent provider: Unknown (0)')
    expect(bubble).not.toHaveTextContent('Turn ended (1.1s)')
  })

  it('labels an UNSPECIFIED-source message as "unknown" rather than masquerading as agent', () => {
    // An UNSPECIFIED source indicates a persistence defect.
    // sourceLabel must display an anomalous data-role instead of reporting it as agent.
    const msg = makeMsg({
      source: MessageSource.UNSPECIFIED,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('message-bubble')).toHaveAttribute('data-role', 'unknown')
  })
})

// ---------------------------------------------------------------------------
// rawJson (Copy Raw JSON feature)
// ---------------------------------------------------------------------------

describe('raw message bytes', () => {
  it('copies invalid original and supplemental bytes without replacement characters', async () => {
    const message = makeMsg({
      source: MessageSource.AGENT,
      content: new Uint8Array([0xFF, 0xFE]),
      supplementalContent: new Uint8Array([0xFE, 0xFF]),
    })
    render(() => <PreferencesProvider><MessageBubble message={message} /></PreferencesProvider>)
    const copied = await copyRawJson()
    expect(copied.content).toEqual({ compression: ContentCompression.NONE, base64: '//4=' })
    expect(copied.supplemental_content).toEqual({ compression: ContentCompression.NONE, base64: '/v8=' })
    expect(copied.content_decode_failed).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Thinking message toolbar buttons (Quote / Copy Markdown)
// ---------------------------------------------------------------------------

describe('thinking message toolbar buttons', () => {
  it('shows Quote and Copy Markdown buttons for thinking messages', () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: 'Let me think about this...' }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    expect(screen.queryByTestId('message-quote')).toBeInTheDocument()
    expect(screen.queryByTestId('message-copy-markdown')).toBeInTheDocument()
  })

  it('orders the toolbar timestamp, Copy Raw JSON, Copy Markdown, then Quote', () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: 'Order matters.' }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    // Agent rows and tool rows share one left-to-right action order.

    const toolbar = screen.getByTestId('message-toolbar')
    const ids = [...toolbar.querySelectorAll('[data-testid]')].map(el => el.getAttribute('data-testid'))
    expect(ids).toEqual(['message-copy-json', 'message-copy-markdown', 'message-quote'])
    // The timestamp leads, ahead of every button.
    const timestamp = toolbar.querySelector(`.${toolHeaderTimestamp}`)
    expect(timestamp).not.toBeNull()
    expect(timestamp!.compareDocumentPosition(screen.getByTestId('message-copy-json')))
      .toBe(Node.DOCUMENT_POSITION_FOLLOWING)
  })

  it('keeps the mirrored order on a user row: Quote first, then the timestamp', () => {
    // A user row reverses its toolbar order. Quote stays closest to its bubble. Preserve that
    // order independently of the agent row.

    const msg = makeMsg({
      source: MessageSource.USER,
      content: rawContent({ content: 'ping' }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    const toolbar = screen.getByTestId('message-toolbar')
    const ids = [...toolbar.querySelectorAll('[data-testid]')].map(el => el.getAttribute('data-testid'))
    expect(ids).toEqual(['message-quote', 'message-copy-markdown', 'message-copy-json'])
    const timestamp = toolbar.querySelector(`.${toolHeaderTimestamp}`)
    expect(timestamp).not.toBeNull()
    expect(timestamp!.compareDocumentPosition(screen.getByTestId('message-quote')))
      .toBe(Node.DOCUMENT_POSITION_PRECEDING)
  })

  it('gives a message and a thought the same chrome-less band content class', () => {
    const bandContent = (inner: unknown) => {
      const { unmount } = render(() => (
        <PreferencesProvider>
          <MessageBubble message={makeMsg({ source: MessageSource.AGENT, content: rawContent(inner) })} />
        </PreferencesProvider>
      ))
      const cls = screen.getByTestId('message-bubble').className
      unmount()
      return cls
    }

    // The row owns the band's background and borders. Its content element carries no bubble
    // decoration.

    const thought = bandContent({ type: 'assistant', message: { content: [{ type: 'thinking', thinking: 'hmm' }] } })
    const text = bandContent({ type: 'assistant', message: { content: [{ type: 'text', text: 'hello' }] } })
    expect(text).toContain(chatStyles.bandMessage)
    expect(thought).toBe(text)
    expect(text).not.toContain(chatStyles.messageBubble)
  })

  it('keeps inert Quote and Copy Markdown button slots during premeasure', async () => {
    const onReply = vi.fn()
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: 'Premeasure should keep toolbar geometry.' }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={onReply} premeasureMode />
      </PreferencesProvider>
    ))

    const quote = screen.getByTestId('message-quote')
    const copy = screen.getByTestId('message-copy-markdown')
    expect(quote).toBeInTheDocument()
    expect(copy).toBeInTheDocument()

    fireEvent.click(quote)
    fireEvent.click(copy)
    await Promise.resolve()

    expect(onReply).not.toHaveBeenCalled()
    expect(clipboardContent).toBeNull()
  })

  it('copies thinking content to clipboard via Copy Markdown', async () => {
    const thinkingText = 'Let me think step by step about this problem.'
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    const copyBtn = screen.getByTestId('message-copy-markdown')
    fireEvent.click(copyBtn)
    await waitFor(() => expect(clipboardContent).not.toBeNull())
    expect(clipboardContent).toBe(thinkingText)
  })
})

describe('premeasure tool action geometry', () => {
  it('keeps an inert in-flow tool-use raw JSON action slot during premeasure', async () => {
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(askUserQuestionToolUse([{ header: 'Premeasure' }])),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} premeasureMode />
      </PreferencesProvider>
    ))

    const copyJson = screen.getByTestId('message-copy-json')
    expect(copyJson).toBeInTheDocument()

    fireEvent.click(copyJson)
    await Promise.resolve()

    expect(clipboardContent).toBeNull()
  })
})

describe('thinking message expansion preference', () => {
  function renderThinkingBubble(thinkingText: string) {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))
  }

  it('shows thinking content by default when expandAgentThoughts is enabled', () => {
    renderThinkingBubble('Expanded thinking content')

    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.getByText('Expanded thinking content')).toBeInTheDocument()
  })

  it('starts collapsed when expandAgentThoughts is disabled and toggles on click', () => {
    localStorageSet(KEY_BROWSER_PREFS, { expandAgentThoughts: false })

    renderThinkingBubble('Collapsed by preference')

    expect(screen.getByText('Thinking')).toBeInTheDocument()
    expect(screen.queryByText('Collapsed by preference')).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.getByText('Collapsed by preference')).toBeInTheDocument()

    fireEvent.click(screen.getByText('Thinking'))
    expect(screen.queryByText('Collapsed by preference')).not.toBeInTheDocument()
  })

  it('updates untouched thinking bubbles when the global preference changes', () => {
    const thinkingText = 'Follows current preference'
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    function TestHarness() {
      const prefs = usePreferences()
      return (
        <>
          <button onClick={() => prefs.setExpandAgentThoughts(false)}>collapse-default</button>
          <button onClick={() => prefs.setExpandAgentThoughts(true)}>expand-default</button>
          <MessageBubble message={msg} onReply={() => {}} />
        </>
      )
    }

    render(() => (
      <PreferencesProvider>
        <TestHarness />
      </PreferencesProvider>
    ))

    expect(screen.getByText(thinkingText)).toBeInTheDocument()

    fireEvent.click(screen.getByText('collapse-default'))
    expect(screen.queryByText(thinkingText)).not.toBeInTheDocument()

    fireEvent.click(screen.getByText('expand-default'))
    expect(screen.getByText(thinkingText)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// rawJson (Copy Raw JSON feature)
// ---------------------------------------------------------------------------

describe('messageBubble rawJson', () => {
  it('copies consolidated content and received metadata without a history request', async () => {
    const history = [
      vi.spyOn(workerRpc, 'listAgentMessages').mockRejectedValue(new Error('Copy must not request message history.')),
      vi.spyOn(workerRpc, 'getAgentMessage').mockRejectedValue(new Error('Copy must not request a stored message.')),
      vi.spyOn(workerRpc, 'getAgentSpanMessages').mockRejectedValue(new Error('Copy must not request a stored span.')),
    ]
    const contentText = '{"type":"notification_thread","old_seqs":[5,8],"messages":[{"type":"settings_changed","changes":{"model":{"old":"A","new":"B"}}},{"type":"interrupted"}]}'
    const supplementText = '{"provider":{"notification_entries":"provider-owned","native_literal":9007199254740993},"metadata":{"duration_ms":0,"opaque_records":[{"data_base64":"AP+A"}],"unrelated":{"key":"first","key":"second"}}}'
    try {
      const msg = makeMsg({
        source: MessageSource.LEAPMUX,
        content: new TextEncoder().encode(contentText),
        supplementalContent: new TextEncoder().encode(supplementText),
        supplementalRevision: 9007199254740993n,
      })
      render(() => <PreferencesProvider><MessageBubble message={msg} /></PreferencesProvider>)
      expect(screen.getByTestId('message-content')).toHaveTextContent('Model (A → B)')
      expect(screen.getByTestId('message-content')).toHaveTextContent('Interrupted')
      const envelope = await copyRawJson()
      expect(envelope.content).toEqual(JSON.parse(contentText))
      expect(envelope.supplemental_content).toEqual(JSON.parse(supplementText))
      expect(envelope.supplemental_revision).toBe('9007199254740993')
      expect(clipboardContent).toMatch(/"native_literal"\s*:\s*9007199254740993(?=[,\s}])/)
      expect(clipboardContent).toContain('provider-owned')
      expect(clipboardContent).toMatch(/"data_base64"\s*:\s*"AP\+A"/)
      expect(clipboardContent?.match(/"key"\s*:/g)).toHaveLength(2)
      expect(clipboardContent).toMatch(/"key"\s*:\s*"first"/)
      expect(clipboardContent).toMatch(/"key"\s*:\s*"second"/)
      for (const request of history)
        expect(request).not.toHaveBeenCalled()
    }
    finally {
      for (const request of history)
        request.mockRestore()
    }
  })

  it('includes content as object for non-LEAPMUX messages', async () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Single' }] },
    }
    const msg = makeMsg({
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(envelope).toHaveProperty('content')
    expect((envelope.content as Record<string, unknown>).type).toBe('assistant')
  })

  it('includes old_seqs from LEAPMUX notification wrapper', async () => {
    const parentMsg = {
      type: 'assistant',
      message: { content: [{ type: 'tool_use', id: 'toolu_1', name: 'Bash', input: { command: 'ls' } }] },
    }
    const childMsg = {
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'file.txt' }] },
    }
    const msg = makeMsg({
      source: MessageSource.LEAPMUX,
      content: wrapContent([parentMsg, childMsg], [5, 8]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    const content = envelope.content as { old_seqs: number[], messages: unknown[] }
    expect(content.old_seqs).toEqual([5, 8])
    expect(content.messages.length).toBe(2)
  })

  it('renders the raw JSON block without crashing when span_lines is malformed', async () => {
    // The backend normally supplies valid span_lines JSON. The debug view must still show a
    // corrupt value without a render error. A hidden system-init row exercises that view.

    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent({ type: 'system', subtype: 'init', cwd: '/repo' }),
      spanLines: '[{"span_id": "broken"', // truncated -> JSON.parse throws
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const content = screen.getByTestId('message-content')
    // Require the Raw JSON token spans. The ErrorBoundary failure view must remain absent.
    expect(content.querySelector(`.${chatStyles.hiddenMessageJson}`)).toBeInTheDocument()
    expect(content).not.toHaveTextContent('Failed to render message')

    // Copy Raw JSON keeps the unparseable span_lines as its raw string.
    const envelope = await copyRawJson()
    expect(envelope.span_lines).toBe('[{"span_id": "broken"')
  })

  it('uses toolbar copy for hidden raw JSON instead of injecting an inline pre copy button', async () => {
    await withControlledCopyCallbacks(async (callbacks) => {
      const msg = makeMsg({
        source: MessageSource.AGENT,
        content: rawContent({ type: 'system', subtype: 'init', cwd: '/repo' }),
      })

      render(() => (
        <PreferencesProvider>
          <MessageBubble message={msg} />
        </PreferencesProvider>
      ))

      const content = screen.getByTestId('message-content')
      expect(content.querySelector(`.${chatStyles.hiddenMessageJson}`)).toBeInTheDocument()

      const toolbar = screen.getByTestId('message-toolbar')
      expect(toolbar.querySelector('[data-testid="message-copy-json"]')).toBeInTheDocument()

      // Raw JSON uses token spans. Run both injection paths before checking that no code button appears.
      expect(callbacks.runIdle()).toBeGreaterThan(0)
      content.appendChild(document.createTextNode(''))
      expect(callbacks.deliverMutations(content)).toBeGreaterThan(0)
      expect(callbacks.runIdle()).toBeGreaterThan(0)
      expect(content.querySelector('.copy-code-button')).not.toBeInTheDocument()
      expect(content.querySelector(`.${codeCopyHostClass}`)).not.toBeInTheDocument()
    })
  })

  it('does not inject a partial-copy button into a large plain-text Markdown display', async () => {
    await withControlledCopyCallbacks(async (callbacks) => {
      const msg = makeMsg({
        source: MessageSource.AGENT,
        content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: `MARKDOWN_HEAD${'x'.repeat(100_000)}MARKDOWN_TAIL` }] } }),
      })

      render(() => (
        <PreferencesProvider>
          <MessageBubble message={msg} />
        </PreferencesProvider>
      ))

      const content = screen.getByTestId('message-content')
      expect(content.querySelector('pre[data-large-text-display]')).toBeInTheDocument()
      expect(callbacks.runIdle()).toBeGreaterThan(0)
      content.appendChild(document.createTextNode(''))
      expect(callbacks.deliverMutations(content)).toBeGreaterThan(0)
      expect(callbacks.runIdle()).toBeGreaterThan(0)
      expect(content.querySelector('.copy-code-button')).not.toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Copy Markdown', hidden: true })).toBeInTheDocument()
    })
  })

  it('re-injects the copy button after the content re-renders (async highlight swap)', async () => {
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: '```js\nconst x = 1\n```' }] } }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const content = screen.getByTestId('message-content')
    // The code block renders and the copy button is injected (deferred to idle).
    const pre = await waitFor(() => {
      const p = content.querySelector('pre')
      expect(p).toBeInTheDocument()
      expect(p!.querySelector('.copy-code-button')).toBeInTheDocument()
      return p!
    })

    // Replace the code block with highlighted HTML. The actual mutation observer must restore the
    // injected Copy button.

    const mdBody = pre.parentElement!
    mdBody.innerHTML = '<pre class="shiki"><code class="language-js">const x = 1</code></pre>'
    expect(content.querySelector('.copy-code-button')).not.toBeInTheDocument()

    await waitFor(() => {
      const swapped = content.querySelector('pre')!
      expect(swapped.querySelector('.copy-code-button')).toBeInTheDocument()
      expect(swapped.classList.contains(codeCopyHostClass)).toBe(true)
    })
  })

  it('does not re-inject the copy button when it is clicked (so its "Copied" state survives)', async () => {
    await withControlledCopyCallbacks(async (callbacks) => {
      const msg = makeMsg({
        source: MessageSource.AGENT,
        content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: '```js\nconst x = 1\n```' }] } }),
      })

      render(() => (
        <PreferencesProvider>
          <MessageBubble message={msg} />
        </PreferencesProvider>
      ))

      const content = screen.getByTestId('message-content')
      expect(callbacks.runIdle()).toBeGreaterThan(0)
      const btn = await waitFor(() => {
        const b = content.querySelector('.copy-code-button')
        expect(b).toBeInTheDocument()
        return b!
      })

      // Deliver the actual Copy-to-Check mutation. The handler must preserve the button and its feedback.
      fireEvent.click(btn)
      await waitFor(() => expect(btn).toHaveAccessibleName('Copied'))
      expect(callbacks.deliverMutations(content)).toBeGreaterThan(0)
      expect(callbacks.runIdle()).toBe(0)
      expect(content.querySelector('.copy-code-button')).toBe(btn)
      expect(btn).toHaveAccessibleName('Copied')
    })
  })
})

// ---------------------------------------------------------------------------
// Notification rendering + raw-JSON fallback
// ---------------------------------------------------------------------------

describe('notification rendering', () => {
  it('renders a recognized notification as its label, not raw JSON', () => {
    const parent = { type: 'settings_changed', changes: { model: { old: 'A', new: 'B' } } }
    const msg = makeMsg({ source: MessageSource.AGENT, content: rawContent(parent) })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Model')
    // Require the notification label instead of the raw payload.
    expect(bubble).not.toHaveTextContent('settings_changed')
  })

  it('falls back to the last-resort card when a notification produces no renderable entries', () => {
    // An empty settings change produces no notification entry. The fallback card must preserve
    // that row and expose its frame through Expand.

    const parent = { type: 'settings_changed', changes: {} }
    const msg = makeMsg({ source: MessageSource.AGENT, content: rawContent(parent) })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('LeapMux has no display for this row')
    expect(bubble).not.toHaveTextContent('settings_changed')
  })

  it('renders a consolidated notification thread, then falls back only when empty', () => {
    // A wrapper with several messages renders every recognized entry.
    const msg = makeMsg({
      source: MessageSource.LEAPMUX,
      content: wrapContent([{ type: 'interrupted' }, { type: 'context_cleared' }]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Interrupted')
    expect(bubble).toHaveTextContent('Context cleared')
  })
})

// ---------------------------------------------------------------------------
// Helper: build TodoWrite tool_use message
// ---------------------------------------------------------------------------

function todoWriteToolUse(todos: Array<{ content: string, status: string, activeForm: string }>) {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_todo_1',
        name: 'TodoWrite',
        input: { todos },
      }],
    },
  }
}

// ---------------------------------------------------------------------------
// TodoWrite collapse/expand
// ---------------------------------------------------------------------------

describe('todoWrite collapse/expand', () => {
  it('shows title with task count when collapsed', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
      { content: 'Task B', status: 'pending', activeForm: 'Working on B' },
      { content: 'Task C', status: 'pending', activeForm: 'Working on C' },
    ])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('3 tasks')
  })

  it('always shows TodoList (alwaysVisible)', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
      { content: 'Task B', status: 'pending', activeForm: 'Working on B' },
    ])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // alwaysVisible keeps the tasks visible.
    expect(bubble).toHaveTextContent('Task A')
    expect(bubble).toHaveTextContent('Task B')
  })

  it('shows all task statuses in TodoList', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'completed', activeForm: 'Working on A' },
      { content: 'Task B', status: 'in_progress', activeForm: 'Running tests' },
      { content: 'Task C', status: 'pending', activeForm: 'Working on C' },
    ])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Task A')
    expect(bubble).toHaveTextContent('Running tests')
    expect(bubble).toHaveTextContent('Task C')
  })

  it('hides expand/collapse button (alwaysVisible)', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'completed', activeForm: 'Working on A' },
      { content: 'Task B', status: 'in_progress', activeForm: 'Running tests' },
    ])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // alwaysVisible hides the Expand button.
    expect(screen.queryByRole('button', { name: 'Expand 1 tool result' })).not.toBeInTheDocument()
    expect(screen.queryAllByRole('button', { name: /^(?:Expand|Collapse)(?:\s|$)/u, hidden: true })).toHaveLength(0)
    cleanup()
    const control = makeMsg({
      agentProvider: AgentProvider.CODEX,
      source: MessageSource.AGENT,
      spanId: 'expand-control',
      content: rawContent({ item: { id: 'expand-control', type: 'mcpToolCall', status: 'completed', server: 'docs', tool: 'lookup', arguments: {}, result: { content: [{ type: 'text', text: Array.from({ length: 8 }, (_, index) => `Line ${index}`).join('\n') }] } } }),
    })
    render(() => <PreferencesProvider><MessageBubble message={control} /></PreferencesProvider>)
    expect(screen.getByRole('button', { name: /^Expand(?:\s|$)/u, hidden: true })).toBeInTheDocument()
  })

  it('body has left border (always visible)', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
    ])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // The body displays its left border before expansion.
    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Helper: build TaskOutput tool_use message
// ---------------------------------------------------------------------------

function taskOutputToolUse() {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_task_1',
        name: 'TaskOutput',
        input: { task_id: 'task-123', block: true, timeout: 30000 },
      }],
    },
  }
}

// ---------------------------------------------------------------------------
// TaskOutput rendering
// ---------------------------------------------------------------------------

describe('taskOutput rendering', () => {
  it('shows waiting state for standalone tool_use', () => {
    const parent = taskOutputToolUse()
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Waiting for output')
  })

  it('hides metadata when no child result', () => {
    const parent = taskOutputToolUse()
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).not.toHaveTextContent('task_id:')
  })
})

// ---------------------------------------------------------------------------
// AskUserQuestion left border
// ---------------------------------------------------------------------------

describe('askUserQuestion left border', () => {
  it('body has left border', () => {
    const parent = askUserQuestionToolUse([{ header: 'Auth' }])
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Header-only renderers (regression)
// ---------------------------------------------------------------------------

describe('header-only renderers', () => {
  it('enterPlanMode renders header only', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_plan_1',
          name: 'EnterPlanMode',
          input: {},
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // Display the session mode with the row kind's own label.
    expect(bubble).toHaveTextContent('plan')
  })

  it('skill renders header only', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_skill_1',
          name: 'Skill',
          input: { skill: 'commit' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Skill: /commit')
  })

  it('agent renders header with description (no child result)', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_agent_1',
          name: 'Agent',
          input: { description: 'Search codebase', subagent_type: 'Explore', prompt: 'Find auth files' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Search codebase')
    expect(bubble).toHaveTextContent('Explore')
  })
})

// ---------------------------------------------------------------------------
// Grep result summary
// ---------------------------------------------------------------------------

describe('grep result summary', () => {
  it('shows pattern in header (no child result)', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_grep_1',
          name: 'Grep',
          input: { pattern: 'TODO', path: '/home/user/project' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('TODO')
    expect(bubble).toHaveTextContent('/home/user/project')
  })
})

// ---------------------------------------------------------------------------
// Glob result summary
// ---------------------------------------------------------------------------

describe('glob result summary', () => {
  it('shows pattern in header (no child result)', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_glob_1',
          name: 'Glob',
          input: { pattern: '**/*.tsx' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('**/*.tsx')
  })
})

// ---------------------------------------------------------------------------
// Agent stats summary
// ---------------------------------------------------------------------------

describe('agent stats summary', () => {
  it('shows description without stats when no child result', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_agent_2',
          name: 'Agent',
          input: { description: 'Search files', subagent_type: 'Explore', prompt: 'Find auth' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Search files')
    expect(bubble).toHaveTextContent('Explore')
    // An absent child result supplies no statistics or Complete label.
    expect(bubble).not.toHaveTextContent('Complete')
    expect(bubble).not.toHaveTextContent('tokens')
    expect(bubble).not.toHaveTextContent('tool uses')
  })

  it('formats title with subagent prefix', () => {
    const innerMsg = {
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use',
          id: 'toolu_agent_3',
          name: 'Agent',
          input: { description: 'Explore message classification', subagent_type: 'Explore', prompt: 'Find classifiers' },
        }],
      },
    }
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('Explore: message classification')
  })
})

describe('plan execution bubble', () => {
  /**
   * Plan approval enqueues PLAN_EXECUTION input. inputqueue.transcriptContent writes its
   * USER-source content with planExecution set to true.
   */
  function planExecutionMsg(id = 'plan-1') {
    return makeMsg({
      id,
      source: MessageSource.USER,
      content: rawContent({ content: 'Execute the following plan:\n\n---\n\nStep one.', planExecution: true }),
    })
  }

  function renderPlanExecution(id?: string) {
    const msg = planExecutionMsg(id)
    const view = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))
    return { msg, view }
  }

  it('wears its own dashed card and runs it to the right panel edge', () => {
    // The plan card uses a mirrored user row also. Its flush-right class must reach the panel edge
    // without another gutter.

    const { view } = renderPlanExecution()
    const bubble = view.getByTestId('message-bubble')
    expect(bubble).toHaveClass(chatStyles.planExecutionMessage)
    expect(bubble).toHaveClass(chatStyles.bubbleFlushRight)
  })

  it('sits in a row widened to the panel edge, so the bleed is not clipped', () => {
    // Paint containment on the virtual row clips a descendant to the row's padding
    // box. The marker alone produces nothing without this.
    const { msg } = renderPlanExecution('plan-2')
    expect(classifyAgentMessage(msg).kind).toBe('plan_execution')
    expect(messageRowChromeClass(classifyAgentMessage(msg).kind, msg.source)).toBe(chatStyles.bleedRow)
  })

  it('reaches the edge exactly as a typed user message does', () => {
    // Both cards use the same layout rule. Check both cards together.
    // A change to only one card must fail these assertions.
    const { view } = renderPlanExecution('plan-3')
    const planBubble = view.getByTestId('message-bubble')
    const planFlushes = planBubble.classList.contains(chatStyles.bubbleFlushRight)
    view.unmount()

    const typed = makeMsg({ id: 'typed-1', source: MessageSource.USER, content: rawContent({ content: 'hello' }) })
    const typedView = render(() => (
      <PreferencesProvider>
        <MessageBubble message={typed} />
      </PreferencesProvider>
    ))
    expect(planFlushes).toBe(typedView.getByTestId('message-bubble').classList.contains(chatStyles.bubbleFlushRight))
    expect(planFlushes).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Helper: build Edit/Write tool_use messages
// ---------------------------------------------------------------------------

function editToolUse(oldString: string, newString: string, filePath = '/src/app.ts') {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_edit_1',
        name: 'Edit',
        input: { file_path: filePath, old_string: oldString, new_string: newString },
      }],
    },
  }
}

function writeToolUse(content: string, filePath = '/src/new-file.ts') {
  return {
    type: 'assistant',
    message: {
      content: [{
        type: 'tool_use',
        id: 'toolu_write_1',
        name: 'Write',
        input: { file_path: filePath, content },
      }],
    },
  }
}

// ---------------------------------------------------------------------------
// Edit/Write tool_use rendering
// ---------------------------------------------------------------------------

describe('edit/write tool_use rendering', () => {
  it('edit shows file path in header', () => {
    const parent = editToolUse('const a = 1', 'const a = 2')
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('app.ts')
  })

  it('write shows file path in header', () => {
    const parent = writeToolUse('export const hello = "world"')
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('new-file.ts')
  })

  it('write with empty content renders without error', () => {
    const parent = writeToolUse('')
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(parent),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // Empty content without a child result supplies no diff view.
    const diffView = container.querySelector('[data-diff-view]')
    expect(diffView).not.toBeInTheDocument()
    expect(container.querySelector('[data-file-diff]')).not.toBeInTheDocument()
    expect(screen.getByTestId('message-content')).toHaveTextContent('new-file.ts')
    expect(screen.getByTestId('message-content')).not.toHaveTextContent('Failed to render message')
    cleanup()
    const nonempty = makeMsg({ source: MessageSource.AGENT, content: rawContent(writeToolUse('export const control = true')) })
    const positive = render(() => <PreferencesProvider><MessageBubble message={nonempty} /></PreferencesProvider>)
    expect(positive.container.querySelector('[data-file-diff]')).toBeInTheDocument()
  })

  describe('row context menu', () => {
    // The jsdom popover stubs come from vitest.setup.ts, which runs before
    // every test file.

    function renderWithHost(msg: ReturnType<typeof makeMsg>, opts: { premeasureMode?: boolean } = {}) {
      return render(() => (
        <PreferencesProvider>
          <MessageContextMenuHostProvider>
            <MessageBubble message={msg} onReply={() => {}} {...(opts.premeasureMode !== undefined ? { premeasureMode: opts.premeasureMode } : {})} />
          </MessageContextMenuHostProvider>
        </PreferencesProvider>
      ))
    }

    function agentMsg() {
      return makeMsg({
        source: MessageSource.AGENT,
        content: rawContent({ type: 'assistant', message: { content: [{ type: 'text', text: 'hello' }] } }),
      })
    }

    /** The row element the gesture attaches to. */
    function rowOf(container: HTMLElement): HTMLElement {
      return container.querySelector(`.${chatStyles.messageRow}`) as HTMLElement
    }

    it('opens the same actions the hover toolbar carries', async () => {
      const { container } = renderWithHost(agentMsg())

      const toolbarIds = [...screen.getByTestId('message-toolbar').querySelectorAll('[data-testid]')]
        .map(el => el.getAttribute('data-testid')!)
        .map(id => id.replace('message-', ''))

      rowOf(container).dispatchEvent(
        new MouseEvent('contextmenu', { clientX: 150, buttons: 0, bubbles: true, cancelable: true }),
      )
      await waitFor(() => expect(screen.queryByTestId('message-context-menu')?.querySelector('[data-testid]')).toBeTruthy())

      const menu = screen.getByTestId('message-context-menu')
      const menuIds = [...menu.querySelectorAll('[data-testid]')]
        .map(el => el.getAttribute('data-testid')!.replace('message-menu-', ''))
        // The menu's information block supplies the toolbar's timestamp.
        // The toolbar uses a plain element without a test ID. Check its text below.
        .filter(id => id !== 'info')

      // The touch menu and mouse toolbar must expose the same actions.
      // Each view controls its own action order.
      expect([...menuIds].sort()).toEqual([...toolbarIds].sort())

      // Both surfaces carry the send time.
      expect(screen.getByTestId('message-toolbar').querySelector(`.${toolHeaderTimestamp}`)).not.toBeNull()
      expect(menu.querySelector('[data-testid="message-menu-info"]')).not.toBeNull()
    })

    it('suppresses the native menu on the row', () => {
      const { container } = renderWithHost(agentMsg())

      const e = new MouseEvent('contextmenu', { clientX: 150, buttons: 0, bubbles: true, cancelable: true })
      rowOf(container).dispatchEvent(e)

      expect(e.defaultPrevented).toBe(true)
    })

    it('arms no gesture on a hidden premeasure copy', () => {
      const { container } = renderWithHost(agentMsg(), { premeasureMode: true })

      const e = new MouseEvent('contextmenu', { clientX: 150, buttons: 0, bubbles: true, cancelable: true })
      rowOf(container).dispatchEvent(e)

      expect(e.defaultPrevented).toBe(false)
    })

    it('offers no recovery actions for a message that delivered', async () => {
      const { container } = renderWithHost(agentMsg())

      rowOf(container).dispatchEvent(
        new MouseEvent('contextmenu', { clientX: 150, buttons: 0, bubbles: true, cancelable: true }),
      )
      await waitFor(() => expect(screen.queryByTestId('message-menu-copy-json')).toBeInTheDocument())

      expect(screen.queryByTestId('message-menu-retry')).not.toBeInTheDocument()
      expect(screen.queryByTestId('message-menu-delete')).not.toBeInTheDocument()
    })

    it('renders without a host, so a bare bubble is unaffected', () => {
      const { container } = render(() => (
        <PreferencesProvider>
          <MessageBubble message={agentMsg()} onReply={() => {}} />
        </PreferencesProvider>
      ))

      const e = new MouseEvent('contextmenu', { clientX: 150, buttons: 0, bubbles: true, cancelable: true })
      rowOf(container).dispatchEvent(e)

      expect(e.defaultPrevented).toBe(false)
    })
  })
})

// ---------------------------------------------------------------------------
// Tool row toolbar: the kind's own words, and Quote
// ---------------------------------------------------------------------------

describe('tool row toolbar labels', () => {
  // An empty command output leaves the command as the copyable text. The outer toolbar must
  // display Copy Command.

  it('states the kind\'s own words on the outer toolbar Copy button', () => {
    const msg = makeMsg({
      agentProvider: AgentProvider.CODEX,
      source: MessageSource.AGENT,
      spanId: 'codex-exec',
      content: rawContent({ item: { id: 'codex-exec', type: 'commandExecution', status: 'completed', command: 'ls -la', aggregated_output: '', exit_code: 0 } }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    expect(screen.getByRole('button', { name: 'Copy Command', hidden: true })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy', hidden: true })).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Tool row Quote
// ---------------------------------------------------------------------------

describe('tool row quote', () => {
  /** Return the text that Quote supplies after the test renders and clicks one bubble. */
  function quoteOf(msg: ReturnType<typeof makeMsg>): string[] {
    const quoted: string[] = []
    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={text => quoted.push(text)} />
      </PreferencesProvider>
    ))
    fireEvent.click(screen.getByTestId('message-quote'))
    return quoted
  }

  // A running TodoWrite row already supplies a checklist. Offer Copy and Quote before its result
  // arrives.

  it('offers Quote and Copy on a running to-do row', () => {
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(todoWriteToolUse([
        { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
        { content: 'Task B', status: 'completed', activeForm: 'Working on B' },
      ])),
    })

    expect(quoteOf(msg)).toEqual(['> - [ ] Task A\n> - [x] Task B\n\n'])
    expect(screen.getByRole('button', { name: 'Copy', hidden: true })).toBeInTheDocument()
  })

  it('quotes a to-do row that carries a result with the saved checklist', () => {
    const msg = makeMsg({
      agentProvider: AgentProvider.OPENCODE,
      source: MessageSource.AGENT,
      content: rawContent({
        sessionUpdate: 'plan',
        entries: [
          { content: 'plan one', status: 'pending' },
          { content: 'plan two', status: 'completed' },
        ],
      }),
    })

    expect(quoteOf(msg)).toEqual(['> - [ ] plan one\n> - [x] plan two\n\n'])
  })

  it('offers Quote on a grep result row, with the matches as its text', () => {
    const msg = makeMsg({
      source: MessageSource.AGENT,
      spanId: 'toolu_grep_1',
      spanType: 'Grep',
      content: rawContent({
        type: 'user',
        message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_grep_1', content: 'src/a.ts:1:hit\nsrc/b.ts:2:hit' }] },
      }),
    })

    expect(quoteOf(msg)).toEqual(['> src/a.ts:1:hit\n> src/b.ts:2:hit\n\n'])
  })

  // The preview supplies file paths. Quote must supply the diff from copyableContent instead.

  // The preview supplies file paths. Quote must supply the diff from copyableContent instead.
  it('quotes a file-change row with the diff rather than the file path', () => {
    const msg = makeMsg({
      source: MessageSource.AGENT,
      content: rawContent(editToolUse('const a = 1', 'const a = 2')),
    })

    const quoted = quoteOf(msg)
    expect(quoted).toHaveLength(1)
    expect(quoted[0]).toContain('> -const a = 1')
    expect(quoted[0]).toContain('> +const a = 2')
    expect(quoted[0]).not.toBe('> /src/app.ts\n\n')
  })
})
