import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { MessageBubble } from '~/components/chat/MessageBubble'
import * as chatStyles from '~/components/chat/messageStyles.css'
import { toolBodyContent } from '~/components/chat/toolStyles.css'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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

  it('copies thinking content to clipboard via Copy Markdown', async () => {
    const thinkingText = 'Let me think step by step about this problem.'
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
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

describe('thinking message expansion preference', () => {
  function renderThinkingBubble(thinkingText: string) {
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
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
    localStorage.setItem('leapmux:browser-prefs', JSON.stringify({ expandAgentThoughts: false }))

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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
    expect(envelope.role).toBe('assistant')
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
      role: MessageRole.ASSISTANT,
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
    expect(envelope.role).toBe('assistant')
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
      role: MessageRole.LEAPMUX,
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

  it('uses toolbar copy for hidden raw JSON instead of injecting an inline pre copy button', () => {
    const msg = makeMsg({
      role: MessageRole.SYSTEM,
      content: rawContent({ type: 'system', subtype: 'init', cwd: '/repo' }),
    })

    const view = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const content = screen.getByTestId('message-content')
    expect(content.querySelector('pre')).toBeInTheDocument()
    expect(content.querySelector('.copy-code-button')).not.toBeInTheDocument()

    const toolbar = screen.getByTestId('message-toolbar')
    expect(toolbar.querySelector('[data-testid="message-copy-json"]')).toBeInTheDocument()
    expect(view.container.querySelector('[data-code-copy="false"]')).toBeInTheDocument()
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.USER,
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
      role: MessageRole.USER,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
      role: MessageRole.ASSISTANT,
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
