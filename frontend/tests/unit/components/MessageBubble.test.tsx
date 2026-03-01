import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { MessageBubble } from '~/components/chat/MessageBubble'
import { toolBodyContent } from '~/components/chat/toolStyles.css'
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

  it('renders answers as bullet list with markdown answer text', () => {
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
    // Header and answer rendered as markdown unordered list
    expect(bubble.textContent).toContain('Uncommitted:')
    expect(bubble.textContent).toContain('Commit changes')
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

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // Answered question rendered as markdown
    expect(bubble.textContent).toContain('OAuth')
    // Unanswered question shows "Not answered" as plain text
    expect(bubble.textContent).toContain('Not answered')
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
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} onReply={() => {}} />
      </PreferencesProvider>
    ))

    expect(screen.queryByTestId('message-quote')).not.toBeNull()
    expect(screen.queryByTestId('message-copy-markdown')).not.toBeNull()
  })

  it('copies thinking content to clipboard via Copy Markdown', async () => {
    const thinkingText = 'Let me think step by step about this problem.'
    const innerMsg = {
      type: 'assistant',
      message: { content: [{ type: 'thinking', thinking: thinkingText }] },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg]),
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

function todoToolResult() {
  return {
    type: 'user',
    message: {
      content: [{
        type: 'tool_result',
        content: 'Todos have been modified successfully.',
        tool_use_id: 'toolu_todo_1',
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
      content: wrapContent([parent, todoToolResult()]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('3 tasks')
  })

  it('hides TodoList when collapsed', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
      { content: 'Task B', status: 'pending', activeForm: 'Working on B' },
    ])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, todoToolResult()]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    // Task content should NOT be visible when collapsed
    expect(bubble.textContent).not.toContain('Task A')
    expect(bubble.textContent).not.toContain('Task B')
  })

  it('shows summary with in-progress task and progress', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'completed', activeForm: 'Working on A' },
      { content: 'Task B', status: 'in_progress', activeForm: 'Running tests' },
      { content: 'Task C', status: 'pending', activeForm: 'Working on C' },
    ])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, todoToolResult()]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Running tests')
    expect(bubble.textContent).toContain('1/3 completed')
  })

  it('shows TodoList when expanded', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'completed', activeForm: 'Working on A' },
      { content: 'Task B', status: 'in_progress', activeForm: 'Running tests' },
    ])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, todoToolResult()]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // Click the expand button
    const expandBtn = screen.getByTitle('Expand 1 tool result')
    fireEvent.click(expandBtn)

    const bubble = screen.getByTestId('message-content')
    // Task content should be visible when expanded
    expect(bubble.textContent).toContain('Task A')
    expect(bubble.textContent).toContain('Running tests')
  })

  it('body has left border when expanded', () => {
    const parent = todoWriteToolUse([
      { content: 'Task A', status: 'pending', activeForm: 'Working on A' },
    ])
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, todoToolResult()]),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // Click the expand button
    const expandBtn = screen.getByTitle('Expand 1 tool result')
    fireEvent.click(expandBtn)

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeNull()
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

function taskOutputResult(task: Record<string, unknown>) {
  return {
    type: 'user',
    message: {
      content: [{
        type: 'tool_result',
        content: task.output || '',
        tool_use_id: 'toolu_task_1',
      }],
    },
    tool_use_result: { task },
  }
}

// ---------------------------------------------------------------------------
// TaskOutput rendering
// ---------------------------------------------------------------------------

describe('taskOutput rendering', () => {
  it('shows status in header when collapsed', () => {
    const parent = taskOutputToolUse()
    const result = taskOutputResult({
      task_id: 'task-123',
      task_type: 'shell',
      status: 'completed',
      description: 'Build',
      output: 'Build succeeded',
    })
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Complete')
    expect(bubble.textContent).toContain('Build')
  })

  it('hides metadata when collapsed', () => {
    const parent = taskOutputToolUse()
    const result = taskOutputResult({
      task_id: 'task-123',
      task_type: 'shell',
      status: 'completed',
      description: 'Build',
      output: 'Build succeeded',
    })
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).not.toContain('task_id:')
  })

  it('body has left border when expanded', () => {
    const parent = taskOutputToolUse()
    const result = taskOutputResult({
      task_id: 'task-123',
      task_type: 'shell',
      status: 'completed',
      description: 'Build',
      output: 'Build succeeded',
    })
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, result]),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // Click the expand button
    const expandBtn = screen.getByTitle('Expand 1 tool result')
    fireEvent.click(expandBtn)

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeNull()
  })

  it('expanded metadata omits duplicate status and description', () => {
    const parent = taskOutputToolUse()
    const result = taskOutputResult({
      task_id: 'task-123',
      task_type: 'shell',
      status: 'completed',
      description: 'Build',
      output: 'Build succeeded',
      exitCode: 0,
    })
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([parent, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    // Click the expand button
    const expandBtn = screen.getByTitle('Expand 1 tool result')
    fireEvent.click(expandBtn)

    const bubble = screen.getByTestId('message-content')
    // Should show task_id and exitCode in metadata
    expect(bubble.textContent).toContain('task_id: task-123')
    expect(bubble.textContent).toContain('exitCode: 0')
    // Should NOT show status: or description: in metadata (they're already in header)
    expect(bubble.textContent).not.toContain('status: completed')
    expect(bubble.textContent).not.toContain('description: Build')
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

    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeNull()
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
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Entering Plan Mode')
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
      content: wrapContent([innerMsg]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Skill: /commit')
  })

  it('agent renders header with description and status', () => {
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
    const result = {
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          content: 'Found 3 files',
          tool_use_id: 'toolu_agent_1',
        }],
      },
      tool_use_result: { status: 'completed' },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Search codebase')
    expect(bubble.textContent).toContain('Complete')
    expect(bubble.textContent).toContain('Explore')
  })
})

// ---------------------------------------------------------------------------
// Grep result summary
// ---------------------------------------------------------------------------

describe('grep result summary', () => {
  it('shows result summary line from tool result', () => {
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
    const result = {
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          content: 'Found 10 files',
          tool_use_id: 'toolu_grep_1',
        }],
      },
      tool_use_result: { tool_name: 'Grep' },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg, result]),
    })

    const { container } = render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Found 10 files')
    // Summary should be inside a bordered area
    const bodyWrapper = container.querySelector(`.${toolBodyContent}`)
    expect(bodyWrapper).not.toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Glob result summary
// ---------------------------------------------------------------------------

describe('glob result summary', () => {
  it('shows file count summary', () => {
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
    const result = {
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          content: 'src/a.tsx\nsrc/b.tsx\nsrc/c.tsx\nsrc/d.tsx\nsrc/e.tsx',
          tool_use_id: 'toolu_glob_1',
        }],
      },
      tool_use_result: { tool_name: 'Glob' },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('5 files')
  })
})

// ---------------------------------------------------------------------------
// Agent stats summary
// ---------------------------------------------------------------------------

describe('agent stats summary', () => {
  it('shows stats summary when completed', () => {
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
    const result = {
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          content: 'Done',
          tool_use_id: 'toolu_agent_2',
        }],
      },
      tool_use_result: {
        status: 'completed',
        totalDurationMs: 65000,
        totalTokens: 1234,
        totalToolUseCount: 5,
      },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('1m 5s')
    expect(bubble.textContent).toContain('1,234 tokens')
    expect(bubble.textContent).toContain('5 tool uses')
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
    const result = {
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          content: 'Done',
          tool_use_id: 'toolu_agent_3',
        }],
      },
      tool_use_result: { status: 'completed' },
    }
    const msg = makeMsg({
      role: MessageRole.ASSISTANT,
      content: wrapContent([innerMsg, result]),
    })

    render(() => (
      <PreferencesProvider>
        <MessageBubble message={msg} />
      </PreferencesProvider>
    ))

    const bubble = screen.getByTestId('message-content')
    expect(bubble.textContent).toContain('Explore: message classification')
  })
})
