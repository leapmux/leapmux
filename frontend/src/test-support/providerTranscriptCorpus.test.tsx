import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TranscriptFrame } from '~/test-support/messageFactory'
import { waitFor } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { createTranscriptScenario } from '~/test-support/transcriptScenario'
import '~/components/chat/providers/testMocks'

// The provider rendering corpus: the extraction and jsdom-rendering assertions
// the E2E suite used to prove with a browser, a worker and a database. Each
// group replays the provider frames through the transcript scenario -- the real
// parse, resolution, classification, pairing, caches and `MessageBubble` -- and
// asserts on the DOM the app would draw.
//
// What stays in the E2E suite is what only a browser can state: the persisted
// ZCode plan and control-banner separation, one shared-plan case for CSS and
// Markdown, permission arguments loaded from an earlier request, requested
// edits after reload, session isolation with a reused tool ID, and one applied
// edit smoke test. Markdown HTML structure is covered there too; under these
// mocks Markdown renders as its source text, which is what the text assertions
// below read.

const SESSION = 'corpus-session'

function frame(id: string, provider: AgentProvider, spanId: string | undefined, spanType: string | undefined, content: unknown, extra: Partial<TranscriptFrame> = {}): TranscriptFrame {
  return { id, provider, ...(spanId === undefined ? {} : { spanId }), ...(spanType === undefined ? {} : { spanType }), agentSessionId: SESSION, content, ...extra }
}

function messages(...frames: TranscriptFrame[]): AgentChatMessage[] {
  return frames.map((one, index) => makeTranscriptMessage(one, BigInt(index + 1)))
}

/** The trimmed text of one message's bubble. */
async function bubbleText(scenario: ReturnType<typeof createTranscriptScenario>, id: string): Promise<string> {
  const rendered = scenario.renderBubble(id)
  await waitFor(() => {
    if ((rendered.container.textContent ?? '').trim() === '')
      throw new Error('The bubble drew nothing')
  })
  const text = rendered.container.textContent ?? ''
  rendered.unmount()
  return text
}

/** The DOM of one message's bubble, unmounted after `read` returns. */
async function bubbleDom<T>(scenario: ReturnType<typeof createTranscriptScenario>, id: string, read: (container: HTMLElement) => T): Promise<T> {
  const rendered = scenario.renderBubble(id)
  await waitFor(() => {
    if ((rendered.container.textContent ?? '').trim() === '')
      throw new Error('The bubble drew nothing')
  })
  try {
    return read(rendered.container)
  }
  finally {
    rendered.unmount()
  }
}

// ---------------------------------------------------------------------------
// Retained tool completion: the separate message field, over a frame whose own
// bytes still say the call never finished.
// ---------------------------------------------------------------------------

const RETAINED_COMPLETION_PROVIDERS = [AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.PI, AgentProvider.ZCODE] as const

function retainedCompletionFrames(provider: AgentProvider): TranscriptFrame[] {
  const id = `completion-${provider}`
  const output = `retained stdout from provider ${provider}`
  const command = 'printf partial'
  const pair = provider === AgentProvider.CODEX
    ? [
        { item: { type: 'commandExecution', id, command, status: 'inProgress' } },
        { item: { type: 'commandExecution', id, command, status: 'inProgress', aggregatedOutput: output } },
      ]
    : provider === AgentProvider.PI
      ? [
          { type: 'tool_execution_start', toolCallId: id, toolName: 'bash', args: { command } },
          { type: 'tool_execution_end', toolCallId: id, toolName: 'bash', isError: true, result: { content: [{ type: 'text', text: output }] } },
        ]
      : provider === AgentProvider.ZCODE
        ? [
            { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: id, toolName: 'Bash', input: { command } } },
            { type: 'tool.updated', payload: { kind: 'result', toolCallId: id, toolName: 'Bash', result: { success: false, content: output } } },
          ]
        : [
            { sessionUpdate: 'tool_call', toolCallId: id, kind: 'execute', title: 'Run command', status: 'pending', rawInput: { command } },
            { sessionUpdate: 'tool_call_update', toolCallId: id, status: 'in_progress', kind: 'execute', content: [{ type: 'content', content: { type: 'text', text: output } }] },
          ]
  return [
    frame(`${id}-request`, provider, id, undefined, pair[0]),
    frame(`${id}-result`, provider, id, undefined, { ...pair[1], _leapmux: { completion: 'complete' } }, { completion: MessageCompletion.INTERRUPTED }),
  ]
}

describe('retained tool completion renders from the separate message field', () => {
  for (const provider of RETAINED_COMPLETION_PROVIDERS) {
    it(`keeps the retained body and the interrupted outcome (${provider})`, async () => {
      const scenario = createTranscriptScenario({ archive: messages(...retainedCompletionFrames(provider)) })
      const text = await bubbleText(scenario, `completion-${provider}-result`)
      expect(text).toContain(`retained stdout from provider ${provider}`)
      expect(text).toContain('Interrupted')
      expect(text).not.toContain('Text truncated')
      expect(text).not.toContain('Error')
    })
  }
})

// ---------------------------------------------------------------------------
// The to-do checklist, one per provider that sends it.
// ---------------------------------------------------------------------------

const TODO_PROVIDERS = [AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.REASONIX] as const

function todoFrames(provider: AgentProvider): TranscriptFrame[] {
  const todos = [{ content: 'Inspect the shared checklist', status: 'pending' }]
  const spanId = 'todo-call'
  let request: unknown
  let result: unknown
  let spanType: string | undefined = 'TodoWrite'
  if (provider === AgentProvider.CLAUDE_CODE) {
    request = { type: 'assistant', message: { content: [{ type: 'tool_use', id: spanId, name: 'TodoWrite', input: { todos } }] } }
    result = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: spanId, content: 'Todos updated' }] }, tool_use_result: { newTodos: todos } }
  }
  else if (provider === AgentProvider.ZCODE) {
    request = { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: spanId, toolName: 'TodoWrite', input: { todos } } }
    result = { type: 'tool.updated', payload: { kind: 'result', toolCallId: spanId, result: { success: true, content: 'Todos updated' } } }
  }
  else {
    spanType = 'other'
    request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: provider === AgentProvider.REASONIX ? 'todo_write' : 'todowrite', kind: 'other', status: 'pending', rawInput: { todos } }
    result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', rawOutput: { metadata: { todos } }, content: [{ type: 'content', content: { type: 'text', text: JSON.stringify(todos) } }] }
  }
  return [
    frame('todo-request', provider, spanId, spanType, request),
    frame('todo-result', provider, spanId, spanType, result),
  ]
}

describe('the to-do result row renders one checklist', () => {
  for (const provider of TODO_PROVIDERS) {
    it(`draws the shared checklist once (${provider})`, async () => {
      const scenario = createTranscriptScenario({ archive: messages(...todoFrames(provider)) })
      // The paired result uses the request's header, so the count draws on the
      // request row and the checklist on the result row.
      const header = await bubbleText(scenario, 'todo-request')
      expect(header).toContain('1 task')
      expect(await bubbleDom(scenario, 'todo-request', container => container.querySelectorAll('[data-task-checkbox]'))).toHaveLength(0)
      const text = await bubbleText(scenario, 'todo-result')
      expect(text).toContain('Inspect the shared checklist')
      expect(text).not.toContain('Todos updated')
      const checkboxes = await bubbleDom(scenario, 'todo-result', container => container.querySelectorAll('[data-task-checkbox]'))
      expect(checkboxes).toHaveLength(1)
      expect(checkboxes[0]?.getAttribute('data-task-checkbox')).toBe('pending')
    })
  }
})

// ---------------------------------------------------------------------------
// Pi's saved to-do tool: list, get, a filtered empty list, a failed update and
// a clear.
// ---------------------------------------------------------------------------

function piTodoFrames(id: string, params: Record<string, unknown>, snapshot: Array<Record<string, unknown>>, error?: string): TranscriptFrame[] {
  return [
    frame(`${id}-request`, AgentProvider.PI, id, 'todo', { type: 'tool_execution_start', toolCallId: id, toolName: 'todo', args: params }),
    frame(`${id}-result`, AgentProvider.PI, id, 'todo', { type: 'tool_execution_end', toolCallId: id, toolName: 'todo', isError: false, result: { content: [{ type: 'text', text: error ?? 'Saved' }], details: { action: params.action, params, tasks: snapshot, nextId: 3, error } } }),
  ]
}

describe('Pi saved todos render through the shared checklist', () => {
  const tasks = [
    { id: 1, subject: 'Inspect sample', status: 'in_progress', activeForm: 'Inspecting sample', description: 'Read **todo sample**.' },
    { id: 2, subject: 'Report findings', status: 'pending' },
  ]

  it('lists the saved tasks with their live statuses', async () => {
    const scenario = createTranscriptScenario({ archive: messages(...piTodoFrames('list', { action: 'list' }, tasks)) })
    const text = await bubbleText(scenario, 'list-result')
    // An in-progress task draws its ACTIVE form; a pending one its subject.
    expect(text).toContain('Inspecting sample')
    expect(text).toContain('Report findings')
    const boxes = await bubbleDom(scenario, 'list-result', container => [...container.querySelectorAll('[data-task-checkbox]')].map(box => box.getAttribute('data-task-checkbox')))
    expect(boxes).toEqual(['in_progress', 'pending'])
  })

  it('renders one task with its note for a get by id', async () => {
    const scenario = createTranscriptScenario({ archive: messages(...piTodoFrames('get', { action: 'get', id: 1 }, tasks)) })
    const dom = await bubbleDom(scenario, 'get-result', container => ({
      boxes: [...container.querySelectorAll('[data-task-checkbox]')].map(box => box.getAttribute('data-task-checkbox')),
      text: container.textContent ?? '',
    }))
    expect(dom.boxes).toEqual(['in_progress'])
    expect(dom.text).toContain('todo sample')
  })

  it('states the filtered empty list', async () => {
    const scenario = createTranscriptScenario({ archive: messages(...piTodoFrames('filtered', { action: 'list', status: 'completed' }, tasks)) })
    expect(await bubbleText(scenario, 'filtered-result')).toContain('No matching tasks')
  })

  it('draws a failed update with the failure icon', async () => {
    const scenario = createTranscriptScenario({ archive: messages(...piTodoFrames('failed', { action: 'update', id: 99, status: 'completed' }, tasks, '#99 not found')) })
    const dom = await bubbleDom(scenario, 'failed-result', container => ({
      icon: container.querySelector('.lucide-circle-alert') !== null,
      text: container.textContent ?? '',
    }))
    expect(dom.icon).toBe(true)
    expect(dom.text).toContain('#99 not found')
  })

  it('states the cleared list', async () => {
    const scenario = createTranscriptScenario({ archive: messages(...piTodoFrames('clear', { action: 'clear' }, [])) })
    expect(await bubbleText(scenario, 'clear-result')).toContain('To-do list cleared')
  })
})

// ---------------------------------------------------------------------------
// ZCode read failures and fetched Markdown, with the request input recovered
// from the supplement.
// ---------------------------------------------------------------------------

describe('ZCode read errors and fetched Markdown', () => {
  it('draws the read failure under the shared outcome word', async () => {
    const scenario = createTranscriptScenario({
      archive: messages(
        frame('read-request', AgentProvider.ZCODE, 'read-failure', 'Read', { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read-failure', toolName: 'Read', inputOmitted: true, inputRef: 'model_stream' } }, { supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'read-failure', input: { file_path: '/project/missing-renderer-fixture.ts' } } } }),
        frame('read-result', AgentProvider.ZCODE, 'read-failure', 'Read', { type: 'tool.updated', payload: { kind: 'error', toolCallId: 'read-failure', error: { message: 'Renderer fixture does not exist' } } }),
      ),
    })
    expect(await bubbleText(scenario, 'read-request')).toContain('/project/missing-renderer-fixture.ts')
    const text = await bubbleText(scenario, 'read-result')
    // The outcome word is the shared one every provider draws ('Error'), not
    // the word ZCode's own transcript used ('Failed').
    expect(text).toContain('Error')
    expect(text).toContain('Renderer fixture does not exist')
  })

  it('renders the fetched page body as Markdown', async () => {
    const scenario = createTranscriptScenario({
      archive: messages(
        frame('fetch-request', AgentProvider.ZCODE, 'fetch', 'WebFetch', { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'fetch', toolName: 'WebFetch', inputOmitted: true, inputRef: 'model_stream' } }, { supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'fetch', input: { url: 'https://example.com', prompt: 'Return the page title' } } } }),
        frame('fetch-result', AgentProvider.ZCODE, 'fetch', 'WebFetch', { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'fetch', result: { success: true, content: '# Native page heading' } } }),
      ),
    })
    expect(await bubbleText(scenario, 'fetch-result')).toContain('Native page heading')
  })
})

// ---------------------------------------------------------------------------
// Shared task lists and fetched Markdown through the Goose and Reasonix
// plugins.
// ---------------------------------------------------------------------------

describe('shared task lists and fetched Markdown', () => {
  it('renders a Goose to-do list as a checklist', async () => {
    const scenario = createTranscriptScenario({
      archive: messages(
        frame('goose-todo', AgentProvider.GOOSE, 'goose-todo', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'goose-todo', status: 'completed', kind: 'edit', rawInput: { content: '- [x] Inspect sources\n- [ ] Verify display' }, _meta: { goose: { toolCall: { toolName: 'todo__todo_write', extensionName: 'todo' } } } }),
      ),
    })
    const dom = await bubbleDom(scenario, 'goose-todo', container => ({
      text: container.textContent ?? '',
      checked: [...container.querySelectorAll('[data-task-checkbox]')].map(box => box.getAttribute('data-task-checkbox')),
    }))
    expect(dom.text).toContain('Inspect sources')
    expect(dom.text).toContain('Verify display')
    expect([...dom.checked].sort()).toEqual(['completed', 'pending'])
  })

  it('renders a Reasonix fetch as Markdown body', async () => {
    const scenario = createTranscriptScenario({
      archive: messages(
        frame('reasonix-fetch', AgentProvider.REASONIX, 'reasonix-fetch', 'fetch', { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-fetch', status: 'completed', title: 'web_fetch', rawInput: { url: 'https://example.com' }, content: [{ type: 'content', content: { type: 'text', text: '## Recovered page title\n\n**Formatted page body**' } }] }),
      ),
    })
    const text = await bubbleText(scenario, 'reasonix-fetch')
    expect(text).toContain('Recovered page title')
    expect(text).toContain('Formatted page body')
  })

  it('keeps a Reasonix receipt edit beside its reported output', async () => {
    const scenario = createTranscriptScenario({
      archive: messages(
        frame('reasonix-receipt', AgentProvider.REASONIX, 'reasonix-receipt', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'reasonix-receipt', status: 'completed', title: 'edit_file', rawInput: { path: '/project/receipt.ts', old_string: 'requested', new_string: 'actualAfter' }, content: [{ type: 'content', content: { type: 'text', text: 'edited /project/receipt.ts (fuzzy match)\nActual replacement receipt after write:\n@@ replacement 1 of 1 (1 occurrence(s), fuzzy match) @@\n-actualBefore\n+actualAfter\n' } }] }),
      ),
    })
    const dom = await bubbleDom(scenario, 'reasonix-receipt', container => ({
      diff: container.querySelector('[data-file-diff]')?.textContent ?? '',
      text: container.textContent ?? '',
    }))
    expect(dom.diff).toContain('actualAfter')
    expect(dom.text).toContain('Fuzzy match')
  })
})

// ---------------------------------------------------------------------------
// The ten-provider applied file-edit matrix.
// ---------------------------------------------------------------------------

function editFrames(provider: AgentProvider): TranscriptFrame[] {
  const path = '/project/parity.ts'
  const before = 'const parityBefore = 1'
  const after = 'const parityAfter = 2'
  const shared = { provider, spanId: 'parity-edit' }
  if (provider === AgentProvider.CLAUDE_CODE) {
    return [
      frame('request', provider, 'parity-edit', 'Edit', { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'parity-edit', name: 'Edit', input: { file_path: path, old_string: before, new_string: after } }] } }),
      frame('result', provider, 'parity-edit', 'Edit', { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'parity-edit', content: 'Saved' }] }, tool_use_result: { filePath: path, oldString: before, newString: after } }),
    ]
  }
  if (provider === AgentProvider.CODEX) {
    return [frame('result', provider, 'parity-edit', 'fileChange', { item: { id: 'parity-edit', type: 'fileChange', status: 'completed', changes: [{ path, kind: { type: 'update' }, diff: `@@ -1 +1 @@\n-${before}\n+${after}\n` }] } })]
  }
  if (provider === AgentProvider.PI) {
    return [
      frame('request', provider, 'parity-edit', 'edit', { type: 'tool_execution_start', toolCallId: 'parity-edit', toolName: 'edit', args: { path, edits: [{ oldText: before, newText: after }] } }),
      frame('result', provider, 'parity-edit', 'edit', { type: 'tool_execution_end', toolCallId: 'parity-edit', toolName: 'edit', result: { content: [{ type: 'text', text: 'Saved' }] }, isError: false }),
    ]
  }
  if (provider === AgentProvider.ZCODE) {
    return [
      frame('request', provider, 'parity-edit', 'Edit', { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', toolName: 'Edit', inputOmitted: true, inputRef: 'model_stream' } }, { supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', input: { file_path: path, old_string: before, new_string: after } } } }),
      frame('result', provider, 'parity-edit', 'Edit', { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'parity-edit', result: { success: true, content: 'Saved', display: { kind: 'file_diff', filePath: path, structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [`-${before}`, `+${after}`] }] } } } }),
    ]
  }
  if (provider === AgentProvider.GITHUB_COPILOT) {
    // Copilot's edit is its own native pair, and `str_replace_editor` states the
    // replacement directly rather than as a diff block.
    const event = (type: string, data: Record<string, unknown>) => ({
      jsonrpc: '2.0',
      method: 'session.event',
      params: { sessionId: 'session-1', event: { id: `${type}-1`, type, data } },
    })
    return [
      frame('request', provider, 'parity-edit', 'str_replace_editor', event('tool.execution_start', { toolCallId: 'parity-edit', toolName: 'str_replace_editor', arguments: { command: 'str_replace', path, old_str: before, new_str: after } })),
      frame('result', provider, 'parity-edit', 'str_replace_editor', event('tool.execution_complete', { toolCallId: 'parity-edit', success: true, result: { content: 'Saved' } })),
    ]
  }
  const input = provider === AgentProvider.GOOSE
    ? { path, before, after }
    : provider === AgentProvider.REASONIX
      ? { path, old_string: before, new_string: after }
      : { filePath: path, oldString: before, newString: after }
  void shared
  return [
    frame('request', provider, 'parity-edit', 'edit', {
      sessionUpdate: 'tool_call',
      toolCallId: 'parity-edit',
      kind: 'edit',
      status: 'pending',
      title: provider === AgentProvider.REASONIX ? 'edit_file' : 'edit',
      rawInput: input,
      ...(provider === AgentProvider.GOOSE ? { _meta: { goose: { toolCall: { toolName: 'edit', extensionName: 'developer' } } } } : {}),
    }),
    frame('result', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'parity-edit', status: 'completed', content: [{ type: 'diff', path, oldText: before, newText: after }] }),
  ]
}

const EDIT_PROVIDERS = [
  ['Claude Code', AgentProvider.CLAUDE_CODE],
  ['Codex', AgentProvider.CODEX],
  ['OpenCode', AgentProvider.OPENCODE],
  ['Kilo', AgentProvider.KILO],
  ['Cursor', AgentProvider.CURSOR],
  ['Copilot', AgentProvider.GITHUB_COPILOT],
  ['Goose', AgentProvider.GOOSE],
  ['Reasonix', AgentProvider.REASONIX],
  ['Pi', AgentProvider.PI],
  ['ZCode', AgentProvider.ZCODE],
] as const

describe('an applied file edit renders its diff', () => {
  for (const [label, provider] of EDIT_PROVIDERS) {
    it(`draws the applied edit for ${label}`, async () => {
      const scenario = createTranscriptScenario({ archive: messages(...editFrames(provider)) })
      // The diff draws on the row that holds the applied change -- the result
      // row for most protocols, the single Codex item, or the request row whose
      // result stated none.
      const rows = provider === AgentProvider.CODEX ? ['result'] : ['request', 'result']
      const diffs: string[] = []
      for (const id of rows)
        diffs.push(await bubbleDom(scenario, id, container => container.querySelector('[data-file-diff]')?.textContent ?? ''))
      const drawn = diffs.find(diff => diff.includes('const parityAfter = 2'))
      expect(drawn, `${label} draws the file diff`).toContain('const parityAfter = 2')
      expect(drawn).toContain('const parityBefore = 1')
    })
  }
})
