import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TranscriptFrame } from '~/test-support/messageFactory'
import { waitFor } from '@solidjs/testing-library'
import { describe, expect, it } from 'vitest'
import { MIMO_TASK_ACTION, MIMO_TASK_STATUS, MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { ALL_PROVIDERS } from '~/generated/contracts/providers'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { makeTranscriptMessage } from '~/test-support/messageFactory'
import { openingFrame, toolFrame } from '~/test-support/mimoFixtures'
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

function frame(id: string, provider: AgentProvider, spanId: string | undefined, spanType: string | undefined, content: unknown, extra: Partial<Omit<TranscriptFrame, 'content' | 'rawContent'>> = {}): TranscriptFrame {
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

const RETAINED_COMPLETION_PROVIDERS = [AgentProvider.CODEX, AgentProvider.OPENCODE, AgentProvider.PI, AgentProvider.ZCODE, AgentProvider.KIMI_CODE, AgentProvider.OH_MY_PI, AgentProvider.MIMO_CODE, AgentProvider.CLINE] as const

function retainedCompletionFrames(provider: AgentProvider): TranscriptFrame[] {
  const id = `completion-${provider}`
  const output = `retained stdout from provider ${provider}`
  const command = 'printf partial'
  const pair = provider === AgentProvider.CODEX
    ? [
        { item: { type: 'commandExecution', id, command, status: 'inProgress' } },
        { item: { type: 'commandExecution', id, command, status: 'inProgress', aggregatedOutput: output } },
      ]
    : provider === AgentProvider.PI || provider === AgentProvider.OH_MY_PI
      ? [
          { type: 'tool_execution_start', toolCallId: id, toolName: 'bash', args: { command } },
          { type: 'tool_execution_end', toolCallId: id, toolName: 'bash', isError: true, result: { content: [{ type: 'text', text: output }] } },
        ]
      : provider === AgentProvider.ZCODE
        ? [
            { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: id, toolName: 'Bash', input: { command } } },
            { type: 'tool.updated', payload: { kind: 'result', toolCallId: id, toolName: 'Bash', result: { success: false, content: output } } },
          ]
        : provider === AgentProvider.KIMI_CODE
          // The turn ended while the call ran, so the worker stored the call's own start
          // again as its closing row, with the output it printed before it stopped.
          ? [
              kimiToolStart(id, 'Bash', { command }),
              { ...kimiToolStart(id, 'Bash', { command }), output },
            ]
          : provider === AgentProvider.MIMO_CODE
            // The worker ends a cut call with its last running update, which states the
            // output so far.
            ? [
                openingFrame(MIMO_TOOL.Bash, { command }, id),
                toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Running, input: { command }, metadata: { output } }, id),
              ]
            : provider === AgentProvider.CLINE
              // The turn ended while the call ran, so the worker closed the call with a
              // result that states the output it printed before it stopped.
              ? [
                  { version: 'v1', event: 'tool.started', sessionId: 's1', payload: { toolCallId: id, toolName: 'run_commands', input: { commands: [command] } } },
                  { version: 'v1', event: 'tool.finished', sessionId: 's1', payload: { toolCallId: id, toolName: 'run_commands', output: [{ query: command, result: output, success: true }] } },
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

const TODO_PROVIDERS = [AgentProvider.CLAUDE_CODE, AgentProvider.ZCODE, AgentProvider.OPENCODE, AgentProvider.KILO, AgentProvider.REASONIX, AgentProvider.CODEWHALE, AgentProvider.KIMI_CODE, AgentProvider.GROK_BUILD, AgentProvider.QWEN_CODE, AgentProvider.OH_MY_PI, AgentProvider.MIMO_CODE, AgentProvider.KIRO] as const

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
  else if (provider === AgentProvider.CODEWHALE) {
    // The runtime numbers the kept checklist and states it in the result's metadata.
    spanType = 'todo_write'
    const identity = { tool_use_id: spanId, tool_name: 'todo_write', tool_input: JSON.stringify({ todos }) }
    request = { event: 'item.started', payload: { item: { kind: 'file_change', metadata: identity }, tool: { id: spanId, name: 'todo_write', input: { todos } } } }
    result = { event: 'item.completed', payload: { item: { kind: 'file_change', detail: 'Todos updated', metadata: { ...identity, task_updates: { checklist: { items: [{ id: 1, ...todos[0] }] } } } } } }
  }
  else if (provider === AgentProvider.KIMI_CODE) {
    spanType = 'TodoList'
    request = kimiToolStart(spanId, 'TodoList', { todos: todos.map(todo => ({ title: todo.content, status: todo.status })) })
    result = kimiToolResult(spanId, 'Todos updated')
  }
  else if (provider === AgentProvider.GROK_BUILD) {
    // Grok states the tool in `_meta["x.ai/tool"]`, and the finished call states
    // the whole list it now holds.
    spanType = 'think'
    request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'todo_write', rawInput: { todos }, _meta: { 'x.ai/tool': { version: 1, name: 'todo_write' } } }
    result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', kind: 'think', rawOutput: { type: 'Todo', TodosUpdated: { todos } } }
  }
  else if (provider === AgentProvider.QWEN_CODE) {
    spanType = 'think'
    request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'TodoWrite', kind: 'think', status: 'in_progress', rawInput: { todos }, _meta: { toolName: 'todo_write' } }
    result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'Todos updated' } }], _meta: { toolName: 'todo_write' } }
  }
  else if (provider === AgentProvider.OH_MY_PI) {
    // omp states the whole list in the RESULT, grouped in phases.
    spanType = 'todo'
    request = { type: 'tool_execution_start', toolCallId: spanId, toolName: 'todo', args: { op: 'init', list: [{ phase: 'Check', items: ['Inspect the shared checklist'] }] } }
    result = { type: 'tool_execution_end', toolCallId: spanId, toolName: 'todo', isError: false, result: { content: [{ type: 'text', text: 'Todos updated' }], details: { op: 'init', phases: [{ name: 'Check', tasks: todos }] } } }
  }
  else if (provider === AgentProvider.KIRO) {
    // Kiro identifies its to-do tool by the title alone, and the finished call
    // states the whole list it now holds.
    spanType = 'other'
    request = { sessionUpdate: 'tool_call', toolCallId: spanId, title: 'Task List', kind: 'other', status: 'in_progress', rawInput: { command: 'create', tasks: { 0: { task_description: 'Inspect the shared checklist' } } } }
    result = { sessionUpdate: 'tool_call_update', toolCallId: spanId, status: 'completed', title: 'Task List', rawOutput: { tasks: [{ id: '1', task_description: 'Inspect the shared checklist', completed: false }] }, content: [{ type: 'content', content: { type: 'text', text: '{"tasks":[]}' } }] }
  }
  else if (provider === AgentProvider.MIMO_CODE) {
    // MiMo's to-do tool is `task`, and one call creates one work item.
    spanType = MIMO_TOOL.Task
    const operation = { operation: { action: MIMO_TASK_ACTION.Create, summary: 'Inspect the shared checklist' } }
    request = openingFrame(MIMO_TOOL.Task, operation, spanId)
    result = toolFrame(MIMO_TOOL.Task, { input: operation, output: 'Created T1 (open): Inspect the shared checklist', metadata: { id: 'T1', status: MIMO_TASK_STATUS.Open } }, spanId)
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
// The applied file-edit matrix, one case for each provider that edits files.
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
  if (provider === AgentProvider.OH_MY_PI) {
    // omp's default hashline edit states no before side; its result keeps both
    // snapshots of the file.
    return [
      frame('request', provider, 'parity-edit', 'edit', { type: 'tool_execution_start', toolCallId: 'parity-edit', toolName: 'edit', args: { input: `[${path}#1A2B]\nPUT 1.=1:\n+${after}` } }),
      frame('result', provider, 'parity-edit', 'edit', { type: 'tool_execution_end', toolCallId: 'parity-edit', toolName: 'edit', result: { content: [{ type: 'text', text: 'Saved' }], details: { path, op: 'update', oldText: `${before}\n`, newText: `${after}\n` } }, isError: false }),
    ]
  }
  if (provider === AgentProvider.ZCODE) {
    return [
      frame('request', provider, 'parity-edit', 'Edit', { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', toolName: 'Edit', inputOmitted: true, inputRef: 'model_stream' } }, { supplemental: { type: 'tool.updated', payload: { kind: 'scheduled', toolCallId: 'parity-edit', input: { file_path: path, old_string: before, new_string: after } } } }),
      frame('result', provider, 'parity-edit', 'Edit', { type: 'tool.updated', payload: { kind: 'result', toolCallId: 'parity-edit', result: { success: true, content: 'Saved', display: { kind: 'file_diff', filePath: path, structuredPatch: [{ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: [`-${before}`, `+${after}`] }] } } } }),
    ]
  }
  if (provider === AgentProvider.CODEWHALE) {
    // The runtime states the landed change as a unified diff in the result's metadata.
    const identity = { tool_use_id: 'parity-edit', tool_name: 'edit', tool_input: JSON.stringify({ path, edits: [{ oldText: before, newText: after }] }) }
    const diff = `--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-${before}\n+${after}\n`
    return [
      frame('request', provider, 'parity-edit', 'edit', { event: 'item.started', payload: { item: { kind: 'file_change', metadata: identity }, tool: { id: 'parity-edit', name: 'edit', input: { path, edits: [{ oldText: before, newText: after }] } } } }),
      frame('result', provider, 'parity-edit', 'edit', { event: 'item.completed', payload: { item: { kind: 'file_change', detail: 'Saved', metadata: { ...identity, mutation: { diff, files: [{ path, outcome: 'updated' }], renames: [] } } } } }),
    ]
  }
  if (provider === AgentProvider.KIMI_CODE) {
    return [
      frame('request', provider, 'parity-edit', 'Edit', kimiToolStart('parity-edit', 'Edit', { path, old_string: before, new_string: after }, { kind: 'diff', path })),
      frame('result', provider, 'parity-edit', 'Edit', kimiToolResult('parity-edit', 'Saved')),
    ]
  }
  if (provider === AgentProvider.AMP) {
    // Amp's `apply_patch` states the patch it asked for, and its result states the
    // unified diff of each file that landed.
    const patchText = `*** Begin Patch\n*** Update File: ${path}\n@@\n-${before}\n+${after}\n*** End Patch`
    const diff = `Index: ${path}\n===================================================================\n--- ${path}\n+++ ${path}\n@@ -1,1 +1,1 @@\n-${before}\n+${after}\n`
    const landed = JSON.stringify({ summary: `update: ${path} (+1/-1)`, files: [{ uri: `file://${path}`, type: 'update', additions: 1, deletions: 1, diff }] })
    return [
      frame('request', provider, 'parity-edit', 'apply_patch', { type: 'assistant', message: { role: 'assistant', content: [{ type: 'tool_use', id: 'parity-edit', name: 'apply_patch', input: { patchText } }] } }),
      frame('result', provider, 'parity-edit', 'apply_patch', { type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'parity-edit', content: landed, is_error: false }] } }),
    ]
  }
  if (provider === AgentProvider.CLINE) {
    // Cline's `editor` states the change it asked for, and its result confirms it.
    return [
      frame('request', provider, 'parity-edit', 'editor', { version: 'v1', event: 'tool.started', sessionId: 's1', payload: { toolCallId: 'parity-edit', toolName: 'editor', input: { path, old_text: before, new_text: after } } }),
      frame('result', provider, 'parity-edit', 'editor', { version: 'v1', event: 'tool.finished', sessionId: 's1', payload: { toolCallId: 'parity-edit', toolName: 'editor', output: { query: `edit:${path}`, result: `Edited ${path}`, success: true } } }),
    ]
  }
  if (provider === AgentProvider.MIMO_CODE) {
    // MiMo states the landed change as a unified diff in the final frame's metadata.
    const input = { file_path: path, old_string: before, new_string: after }
    const diff = `Index: ${path}\n===================================================================\n--- ${path}\n+++ ${path}\n@@ -1,1 +1,1 @@\n-${before}\n+${after}\n`
    return [
      frame('request', provider, 'parity-edit', MIMO_TOOL.Edit, openingFrame(MIMO_TOOL.Edit, input, 'parity-edit')),
      frame('result', provider, 'parity-edit', MIMO_TOOL.Edit, toolFrame(MIMO_TOOL.Edit, { input, output: 'Edit applied successfully.', metadata: { diff, filediff: { file: path, patch: diff, additions: 1, deletions: 1 } } }, 'parity-edit')),
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
  if (provider === AgentProvider.GROK_BUILD) {
    // Grok's first frame states the model's own arguments, its identity in
    // `_meta`, and no kind; the finished frame carries the diff.
    return [
      frame('request', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call', toolCallId: 'parity-edit', title: 'search_replace', rawInput: { file_path: path, old_string: before, new_string: after }, _meta: { 'x.ai/tool': { version: 1, name: 'search_replace' } } }),
      frame('result', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'parity-edit', status: 'completed', kind: 'edit', content: [{ type: 'diff', path, oldText: before, newText: after }] }),
    ]
  }
  if (provider === AgentProvider.KIRO) {
    // Kiro states its own argument keys, and the finished frame carries the diff.
    return [
      frame('request', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call', toolCallId: 'parity-edit', title: 'Replace in File', kind: 'edit', status: 'in_progress', rawInput: { path, oldStr: before, newStr: after }, locations: [{ path }] }),
      frame('result', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'parity-edit', status: 'completed', title: 'Replace in File', rawInput: { path, oldStr: before, newStr: after }, content: [{ type: 'diff', path, oldText: before, newText: after }] }),
    ]
  }
  if (provider === AgentProvider.QWEN_CODE) {
    return [
      frame('request', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call', toolCallId: 'parity-edit', title: 'Edit: parity.ts', kind: 'edit', status: 'in_progress', rawInput: { file_path: path, old_string: before, new_string: after }, _meta: { toolName: 'edit' } }),
      frame('result', provider, 'parity-edit', 'edit', { sessionUpdate: 'tool_call_update', toolCallId: 'parity-edit', status: 'completed', content: [{ type: 'diff', path, oldText: before, newText: after }], _meta: { toolName: 'edit' } }),
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
  ['Codewhale', AgentProvider.CODEWHALE],
  ['Kimi Code', AgentProvider.KIMI_CODE],
  ['Grok Build', AgentProvider.GROK_BUILD],
  ['Kiro', AgentProvider.KIRO],
  ['Qwen Code', AgentProvider.QWEN_CODE],
  ['Oh My Pi', AgentProvider.OH_MY_PI],
  ['MiMo Code', AgentProvider.MIMO_CODE],
  ['Amp', AgentProvider.AMP],
  ['Cline', AgentProvider.CLINE],
] as const

describe('an applied file edit renders its diff', () => {
  // Every provider edits files, so the matrix lists every provider. A new provider
  // that the list omits would otherwise ship with no case, and nothing would say so.
  it('lists every provider once', () => {
    const listed = EDIT_PROVIDERS.map(([, provider]) => provider)
    expect(new Set(listed).size).toBe(listed.length)
    expect([...listed].sort((a, b) => a - b)).toEqual([...ALL_PROVIDERS].sort((a, b) => a - b))
  })

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
