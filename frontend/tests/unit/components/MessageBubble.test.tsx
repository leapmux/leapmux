import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { codeCopyHostClass } from '~/components/chat/markdownEditor/markdownContent.css'
import { MessageBubble } from '~/components/chat/MessageBubble'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { toolBodyContent } from '~/components/chat/toolStyles.css'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { AgentProvider, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { KEY_BROWSER_PREFS, localStorageSet } from '~/lib/browserStorage'
import { makeMessage, rawContent, wrapContent } from '../helpers/messageFactory'

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

// Track clipboard writes for assertions.
let clipboardContent: string | null = null

beforeEach(() => {
  clipboardContent = null
  localStorage.clear()
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

/** Click the "Copy Raw JSON" button and return the parsed clipboard content. */
async function copyRawJson(): Promise<Record<string, unknown>> {
  const btn = screen.getByTestId('message-copy-json')
  fireEvent.click(btn)
  await waitFor(() => expect(clipboardContent).not.toBeNull())
  return JSON.parse(clipboardContent!)
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
    expect(bubble).toHaveTextContent('2 questions')
    expect(bubble).toHaveTextContent('Auth')
    expect(bubble).toHaveTextContent('Database')
  })
})

// ---------------------------------------------------------------------------
// result_divider dispatch
// ---------------------------------------------------------------------------

describe('result_divider dispatch', () => {
  it('renders a turn-end divider for a registered provider result', () => {
    // Happy path: a CLAUDE_CODE result classifies as result_divider and renders
    // through the shared renderResultDivider as the "Took Xs" turn-end row.
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
    expect(bubble).toHaveTextContent('Took 1.1s')
    expect(bubble).not.toHaveTextContent('duration_ms')
  })

  it('marks a result-divider error <pre> (non-markdown) so its copy button positions top-right', async () => {
    // The error detail renders in a bare <pre class={resultErrorDetail}> OUTSIDE
    // `.markdownContent`. injectCopyButtons still adds a copy button to it; the fix is the
    // `code-copy-host` marker, which carries the absolute top-right positioning + relative
    // anchor so the button no longer falls inline at the end of the error text.
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
// unsupported provider (loud-bug surface)
// ---------------------------------------------------------------------------

describe('unsupported_provider rendering', () => {
  it('surfaces a loud error (not a guessed Claude render) for an UNSPECIFIED-provider message', () => {
    // classifyMessage no longer guesses Claude for an unspecified/unregistered
    // provider: the message is classified unsupported_provider and shown as a
    // visible error plus raw JSON, never silently rendered through Claude's
    // renderers. So a `result` envelope here must NOT become a "Took Xs" divider.
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
    // The banner names the provider via agentProviderLabel ("Unknown" for the
    // proto-0 default) alongside the numeric value, not just the bare number.
    expect(bubble).toHaveTextContent('Unsupported agent provider: Unknown (0)')
    expect(bubble).not.toHaveTextContent('Took 1.1s')
  })

  it('labels an UNSPECIFIED-source message as "unknown" rather than masquerading as agent', () => {
    // sourceLabel no longer silently relabels a proto-0 source as 'agent'; an
    // UNSPECIFIED row is a persistence bug and gets a visibly anomalous data-role.
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
  it('includes metadata fields', async () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Hello' }] },
    }
    const msg = makeMsg({
      id: 'msg-meta-1',
      source: MessageSource.AGENT,
      seq: 3n,
      createdAt: '2025-01-15T10:00:00.000Z',
      deliveryError: 'worker offline',
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} error="worker offline" />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(envelope.id).toBe('msg-meta-1')
    expect(envelope.source).toBe('agent')
    expect(envelope.seq).toBe(3)
    expect(envelope.created_at).toBe('2025-01-15T10:00:00.000Z')
    expect(envelope.delivery_error).toBe('worker offline')
    // Raw JSON format uses content (not messages) for non-LEAPMUX messages
    expect(envelope).toHaveProperty('content')
    expect(envelope).not.toHaveProperty('messages')
  })

  it('omits empty optional fields', async () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'No optionals' }] },
    }
    const msg = makeMsg({
      id: 'msg-no-opts',
      source: MessageSource.AGENT,
      deliveryError: '',
      content: rawContent(innerMsg),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(envelope).not.toHaveProperty('delivery_error')
    // Required fields should still be present.
    expect(envelope.id).toBe('msg-no-opts')
    expect(envelope.source).toBe('agent')
    expect(envelope).toHaveProperty('content')
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
    expect(envelope.old_seqs).toEqual([5, 8])
    expect((envelope.messages as unknown[]).length).toBe(2)
  })

  it('renders the raw JSON block without crashing when span_lines is malformed', async () => {
    // span_lines is backend-generated and normally valid JSON, but this envelope
    // also backs the raw-JSON debug surface (hidden / unsupported_provider rows),
    // whose whole purpose is to show the bytes when something is wrong. A corrupt
    // span_lines must degrade to its raw string, not throw into the ErrorBoundary
    // and hide the JSON. A `hidden` system-init message renders that block.
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
    // The raw-JSON block rendered (as token spans), not the ErrorBoundary's failure fallback.
    expect(content.querySelector(`.${chatStyles.hiddenMessageJson}`)).toBeInTheDocument()
    expect(content).not.toHaveTextContent('Failed to render message')

    // Copy Raw JSON keeps the unparseable span_lines as its raw string.
    const envelope = await copyRawJson()
    expect(envelope.span_lines).toBe('[{"span_id": "broken"')
  })

  it('uses toolbar copy for hidden raw JSON instead of injecting an inline pre copy button', async () => {
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

    // The raw JSON renders as token <span>s, not a <pre>, so the copy-button injector
    // (which only targets <pre> elements) never augments it. Injection is deferred to
    // idle, so advance PAST idle to prove no inline copy button or positioning marker
    // appears on the JSON block.
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(content.querySelector('.copy-code-button')).not.toBeInTheDocument()
    expect(content.querySelector(`.${codeCopyHostClass}`)).not.toBeInTheDocument()
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

    // Simulate the async highlight swap: the worker's highlighted HTML replaces the
    // placeholder's innerHTML, wiping the injected button. The observer must re-inject it.
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
    const btn = await waitFor(() => {
      const b = content.querySelector('.copy-code-button')
      expect(b).toBeInTheDocument()
      return b!
    })

    // Clicking copy flips the IconButton's icon (Copy -> Check): a subtree mutation under
    // contentRef. The observer must IGNORE button-internal mutations -- re-injecting would
    // dispose this button mid-click and wipe its transient "Copied" checkmark.
    fireEvent.click(btn)
    await new Promise(resolve => setTimeout(resolve, 20)) // past the idle debounce
    // SAME node still in place -> no re-injection fired for the button's own mutation.
    expect(content.querySelector('.copy-code-button')).toBe(btn)
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
    // The label rendered, not the raw payload.
    expect(bubble).not.toHaveTextContent('settings_changed')
  })

  it('falls back to raw JSON when a notification produces no renderable entries', () => {
    // settings_changed with no actual changes yields zero thread entries. Rather
    // than render an empty bubble, MessageBubble surfaces the raw payload via the
    // last-resort renderer so the message never silently vanishes.
    const parent = { type: 'settings_changed', changes: {} }
    const msg = makeMsg({ source: MessageSource.AGENT, content: rawContent(parent) })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble).toHaveTextContent('settings_changed')
  })

  it('renders a consolidated notification thread, then falls back only when empty', () => {
    // A multi-message wrapper renders every recognized entry...
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
    // Tasks are always visible (alwaysVisible=true)
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

    // Expand button should not exist since alwaysVisible hides it
    expect(screen.queryByRole('button', { name: 'Expand 1 tool result' })).not.toBeInTheDocument()
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

    // Body should have left border without needing to expand
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
    expect(bubble).toHaveTextContent('Entering Plan Mode')
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
    // Without child result, stats and "Complete" should not appear
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

describe('pending user bubble state', () => {
  it('stops pulsation when a local user message has a delivery error', () => {
    const msg = makeMsg({
      id: 'local-1',
      source: MessageSource.USER,
      content: rawContent({ content: 'hello' }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} error="Failed to deliver" />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('message-bubble')).not.toHaveClass(chatStyles.userMessagePending)
  })

  it('keeps pulsation for a local user message without a delivery error', () => {
    const msg = makeMsg({
      id: 'local-2',
      source: MessageSource.USER,
      content: rawContent({ content: 'hello' }),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    expect(screen.getByTestId('message-bubble')).toHaveClass(chatStyles.userMessagePending)
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

    // No diff view should be rendered for empty content without child result
    const diffView = container.querySelector('[data-diff-view]')
    expect(diffView).not.toBeInTheDocument()
  })
})
