import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { acpTextContent, renderACPToolPair } from '../acp/testUtils'
import { input } from '../testUtils'

import '../testMocks'
import './plugin'

/** Grok's first `tool_call`: the name as the title, and its identity in `_meta`. */
function opening(name: string, rawInput: Record<string, unknown>) {
  return { title: name, rawInput, _meta: { 'x.ai/tool': { version: 1, name } } }
}

function bytes(value: string): number[] {
  return [...new TextEncoder().encode(value)]
}

/** One finished call, read through the plugin with its opening frame paired. */
function finishedCall(name: string, rawInput: Record<string, unknown>, frame: Record<string, unknown>) {
  const request = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...opening(name, rawInput) }
  return providerToolCall(AgentProvider.GROK_BUILD, { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...frame }, {
    spanType: 'tool_call_update',
    request: input(request, null, AgentProvider.GROK_BUILD),
  })
}

describe('grok tool rendering', () => {
  it('states the exit code of a shell command from Grok\'s own record', () => {
    const call = finishedCall('run_terminal_command', { command: 'false', description: 'Fail' }, {
      kind: 'execute',
      content: acpTextContent(''),
      rawOutput: { type: 'Bash', output: [], output_for_prompt: 'exit: 1\n', exit_code: 1, signal: null },
    })
    if (call?.kind !== 'execute' || !call.result || !('commands' in call.result))
      throw new Error('a shell call is an execute row')
    expect(call.result.commands[0]).toMatchObject({ exitCode: 1 })
  })

  it('states the signal that ended a shell command', () => {
    const call = finishedCall('run_terminal_command', { command: 'sleep 9' }, {
      kind: 'execute',
      content: acpTextContent('partial'),
      rawOutput: { type: 'Bash', output: bytes('partial'), exit_code: -1, signal: 'SIGTERM' },
    })
    if (call?.kind !== 'execute' || !call.result || !('commands' in call.result))
      throw new Error('a shell call is an execute row')
    expect(call.result.commands[0]).toMatchObject({ signal: 'SIGTERM', output: 'partial' })
    expect(call.result.commands[0]?.exitCode).toBeUndefined()
  })

  it('reads the target of read_file as the file path', () => {
    const call = finishedCall('read_file', { target_file: '/p/a.ts' }, { kind: 'read', content: acpTextContent('1→one\n') })
    expect(call?.kind === 'read' && call.request.path).toBe('/p/a.ts')
  })

  it('draws the directory tree of list_dir as its entries', () => {
    const { container } = renderACPToolPair(AgentProvider.GROK_BUILD, opening('list_dir', { target_directory: '/p' }), {
      kind: 'other',
      rawInput: { variant: 'ListDir', target_directory: '/p' },
      rawOutput: { type: 'ListDir', Content: { content: '- /p/\n  - a.ts\n  - src/\n    - b.ts', absolute_root_path: '/p' } },
    })
    expect(container.textContent).toContain('3 entries')
    expect(container.textContent).toContain('src/b.ts')
  })

  it('draws the grouped matches of grep', () => {
    const call = finishedCall('grep', { pattern: 'needle', path: '/p' }, {
      kind: 'search',
      content: acpTextContent('found 1 matches'),
      rawOutput: { type: 'GrepSearch', stdout: bytes('/p/a.ts\n3:needle\n'), match_count: 1, file_matches: [{ path: '/p/a.ts', matches: [{ line_number: 3, content: 'needle' }] }] },
    })
    if (call?.kind !== 'grep' || !call.result || !('filenames' in call.result))
      throw new Error('a grep call is a grep row')
    expect(call.result.filenames).toEqual(['/p/a.ts'])
    expect(call.result.matchCount).toBe(1)
  })

  it('draws a finished subagent with its report and its id', () => {
    const { container } = renderACPToolPair(AgentProvider.GROK_BUILD, opening('spawn_subagent', { prompt: 'List the files', description: 'List files' }), {
      kind: 'other',
      title: 'List files',
      content: acpTextContent('Done.\n\n<subagent_meta>id=sub-1</subagent_meta>'),
      rawOutput: { type: 'SubagentCompleted', output: '**Listed** two files.', subagent_id: 'sub-1', subagent_type: 'general-purpose', tool_calls: 2, turns: 1, duration_ms: 1500 },
    })
    expect(container.textContent).toContain('Agent "List files" completed')
    expect(container.textContent).toContain('sub-1')
    expect(container.querySelector('strong')?.textContent).toBe('Listed')
    expect(container.textContent).not.toContain('<subagent_meta>')
  })

  it('draws a background subagent as running', () => {
    const call = finishedCall('spawn_subagent', { prompt: 'Say done', description: 'Say done', background: true }, {
      kind: 'other',
      content: acpTextContent('Subagent started in background.\nsubagent_id: sub-2\ndescription: Say done'),
      rawOutput: { type: 'Text', text: 'Subagent started in background.\nsubagent_id: sub-2\ndescription: Say done' },
    })
    if (call?.kind !== 'agent' || !call.result || !('agents' in call.result))
      throw new Error('a subagent launch is an agent row')
    expect(call.request.metadata).toContainEqual({ label: 'Background', value: 'Yes' })
    expect(call.request.registryKey).toBe('call')
    expect(call.result.agents[0]).toMatchObject({ agentId: 'sub-2', outcome: 'running' })
  })

  it('draws a workflow launch as a running agent with its script', () => {
    const call = finishedCall('workflow', { source: 'await agent("x")' }, {
      content: acpTextContent('started'),
      rawOutput: { type: 'Workflow', run_id: 'wf-1', task_id: 'wf-1', name: 'review-changes', script_path: '/s.js', message: 'Workflow review-changes started.' },
    })
    if (call?.kind !== 'agent' || !call.result || !('agents' in call.result))
      throw new Error('a workflow is an agent row')
    expect(call.request).toMatchObject({ description: 'review-changes', prompt: 'await agent("x")', promptLabel: 'Script' })
    expect(call.result.agents[0]).toMatchObject({ agentId: 'wf-1', outcome: 'running', body: 'Workflow review-changes started.' })
  })

  it('draws the whole list Grok holds after a merged todo_write', () => {
    const call = finishedCall('todo_write', { todos: [{ id: '2', content: 'Second', status: 'in_progress' }], merge: true }, {
      kind: 'think',
      rawOutput: { type: 'Todo', TodosUpdated: { todos: [{ content: 'First', status: 'completed' }, { content: 'Second', status: 'in_progress' }] } },
    })
    if (call?.kind !== 'todo' || !call.result || !('items' in call.result))
      throw new Error('a todo call is a todo row')
    expect(call.request.items.map(item => item.content)).toEqual(['Second'])
    expect(call.result.items.map(item => item.content)).toEqual(['First', 'Second'])
  })

  it('draws the MCP call that use_tool wraps', () => {
    const call = finishedCall('use_tool', { tool_name: 'linear__list_issues', tool_input: { team: 'core' } }, { content: acpTextContent('3 issues') })
    expect(call?.kind).toBe('mcp')
    expect(call?.kind === 'mcp' && call.request).toMatchObject({ server: 'linear', tool: 'list_issues', args: { team: 'core' } })
  })

  it('draws a direct MCP tool by its server and tool', () => {
    const request = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', title: 'docs__search', rawInput: { q: 'x' }, _meta: { 'x.ai/tool': { name: 'docs__search', namespace: 'mcp' } } }
    const call = providerToolCall(AgentProvider.GROK_BUILD, request)
    expect(call?.kind === 'mcp' && call.request).toMatchObject({ server: 'docs', tool: 'search' })
  })

  it('draws the questions of ask_user_question with the answer', () => {
    const call = finishedCall('ask_user_question', { questions: [{ question: 'Pick?', options: [{ label: 'A', description: 'a' }], multi_select: true }] }, {
      content: acpTextContent('User has answered your questions: "Pick?"="A".'),
    })
    if (call?.kind !== 'question')
      throw new Error('a question call is a question row')
    expect(call.request.questions).toEqual([{ question: 'Pick?', options: [{ label: 'A', description: 'a' }] }])
    expect(call.result && 'answers' in call.result && call.result.answers[0]?.answer).toContain('"Pick?"="A"')
  })

  it('states the ids that a task call asks about', () => {
    const call = finishedCall('get_command_or_subagent_output', { task_ids: ['a', 'b'], timeout_ms: 500 }, { content: acpTextContent('done') })
    expect(call?.kind === 'task' && call.request).toEqual({ action: 'output', taskId: 'a, b', timeoutMs: 500 })
    expect(call?.kind === 'task' && call.result).toEqual({ outcome: 'completed', output: 'done' })
  })
})
