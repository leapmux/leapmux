import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { MessageBubble } from '~/components/chat/MessageBubble'
import { PreferencesProvider } from '~/context/PreferencesContext'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'

// jsdom does not provide ResizeObserver
beforeAll(() => {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
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

/** Encode a JSON object into a wrapper envelope stored in the content field. */
function wrapContent(messages: unknown[], oldSeqs: number[] = []): Uint8Array {
  const wrapper = { old_seqs: oldSeqs, messages }
  return new TextEncoder().encode(JSON.stringify(wrapper))
}

/** Build a minimal AgentChatMessage for testing. */
function makeMsg(overrides: Partial<{
  id: string
  role: MessageRole
  seq: bigint
  createdAt: string
  updatedAt: string
  deliveryError: string
  content: Uint8Array
  contentCompression: ContentCompression
}>): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: overrides.id ?? 'msg-1',
    role: overrides.role ?? MessageRole.ASSISTANT,
    seq: overrides.seq ?? 1n,
    createdAt: overrides.createdAt ?? '2025-01-15T10:00:00.000Z',
    updatedAt: overrides.updatedAt ?? '',
    deliveryError: overrides.deliveryError ?? '',
    content: overrides.content ?? new Uint8Array(),
    contentCompression: overrides.contentCompression ?? ContentCompression.NONE,
  } as AgentChatMessage
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

function controlResponse(action = 'approved') {
  return { isSynthetic: true, controlResponse: { action, comment: '' } }
}

function toolResultWithAnswers(answers: Record<string, string>) {
  return {
    type: 'user',
    message: {
      content: [{
        type: 'tool_result',
        content: 'User has answered your questions.',
        tool_use_id: 'toolu_ask_1',
      }],
    },
    tool_use_result: { answers },
  }
}

// ---------------------------------------------------------------------------
// AskUserQuestion thread rendering
// ---------------------------------------------------------------------------

describe('askUserQuestion thread rendering', () => {
  it('shows "Submitted answers" when controlResponse precedes tool_result', () => {
    const parent = askUserQuestionToolUse([{ header: 'Uncommitted' }])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([
        parent,
        controlResponse(),
        toolResultWithAnswers({ Uncommitted: 'Commit changes' }),
      ]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Submitted answers')
    expect(bubble.textContent).not.toContain('Waiting for answers')
  })

  it('renders answers as bullet list with bold answer text', () => {
    const parent = askUserQuestionToolUse([{ header: 'Uncommitted' }])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([
        parent,
        controlResponse(),
        toolResultWithAnswers({ Uncommitted: 'Commit changes' }),
      ]),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // Check bullet prefix and header text
    expect(bubble.textContent).toContain('- Uncommitted: ')
    // Check that answer is bolded
    const strong = container.querySelector('strong')
    expect(strong).not.toBeNull()
    expect(strong!.textContent).toBe('Commit changes')
  })

  it('shows "Not answered" for unanswered questions', () => {
    const parent = askUserQuestionToolUse([{ header: 'Auth' }, { header: 'Database' }])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([
        parent,
        controlResponse(),
        toolResultWithAnswers({ Auth: 'OAuth' }),
      ]),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const strongs = container.querySelectorAll('strong')
    const strongTexts = Array.from(strongs).map(el => el.textContent)
    expect(strongTexts).toContain('OAuth')
    expect(strongTexts).toContain('Not answered')
  })

  it('shows "Waiting for answers" with no thread children', () => {
    const parent = askUserQuestionToolUse([{ header: 'Uncommitted' }])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Waiting for answers')
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
      role: MessageRole.ASSISTANT,
      seq: 3n,
      createdAt: '2025-01-15T10:00:00.000Z',
      updatedAt: '2025-01-15T10:05:00.000Z',
      deliveryError: 'worker offline',
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} error="worker offline" />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(envelope.id).toBe('msg-meta-1')
    expect(envelope.role).toBe('assistant')
    expect(envelope.seq).toBe(3)
    expect(envelope.created_at).toBe('2025-01-15T10:00:00.000Z')
    expect(envelope.updated_at).toBe('2025-01-15T10:05:00.000Z')
    expect(envelope.delivery_error).toBe('worker offline')
    expect(Array.isArray(envelope.messages)).toBe(true)
  })

  it('omits empty optional fields', async () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'No optionals' }] },
    }
    const msg = makeMsg({
      id: 'msg-no-opts',
      role: MessageRole.ASSISTANT,
      updatedAt: '',
      deliveryError: '',
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(envelope).not.toHaveProperty('updated_at')
    expect(envelope).not.toHaveProperty('delivery_error')
    // Required fields should still be present.
    expect(envelope.id).toBe('msg-no-opts')
    expect(envelope.role).toBe('assistant')
    expect(envelope).toHaveProperty('messages')
  })

  it('wraps single message in messages array', async () => {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Single' }] },
    }
    const msg = makeMsg({
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const envelope = await copyRawJson()
    expect(Array.isArray(envelope.messages)).toBe(true)
    const messages = envelope.messages as unknown[]
    expect(messages).toHaveLength(1)
    expect((messages[0] as Record<string, unknown>).type).toBe('assistant')
  })

  it('includes old_seqs from thread wrapper', async () => {
    const parentMsg = {
      type: 'assistant',
      message: { content: [{ type: 'tool_use', id: 'toolu_1', name: 'Bash', input: { command: 'ls' } }] },
    }
    const childMsg = {
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: 'file.txt' }] },
    }
    const msg = makeMsg({
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
})
