import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { failedResult, proseResult } from '../../model/toolCall'
import { acpTextContent, renderACPToolPair } from '../acp/testUtils'
import { input } from '../testUtils'

import '../testMocks'
import './plugin'

/** One finished call, read through the plugin with its opening frame paired. */
function finishedCall(name: string, kind: string, rawInput: Record<string, unknown>, frame: Record<string, unknown>) {
  const request = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'in_progress', title: name, kind, rawInput, _meta: { toolName: name } }
  return providerToolCall(AgentProvider.QWEN_CODE, { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', _meta: { toolName: name }, ...frame }, {
    spanType: 'tool_call_update',
    request: input(request, null, AgentProvider.QWEN_CODE),
  })
}

/** One call still in flight: its opening frame alone. */
function openCall(name: string, kind: string, rawInput: Record<string, unknown>) {
  return providerToolCall(AgentProvider.QWEN_CODE, { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'in_progress', title: name, kind, rawInput, _meta: { toolName: name } })
}

const IMAGE_BLOCK = { type: 'content', content: { type: 'image', data: 'aGk=', mimeType: 'image/png' } }

describe('qwen tool rendering', () => {
  it('draws the output and exit of a shell command from its record, not the model text', () => {
    const call = finishedCall('run_shell_command', 'execute', { command: 'false' }, {
      content: acpTextContent('Command: false\nDirectory: (root)\nOutput: (empty)\nError: boom\nExit Code: 1'),
      rawOutput: { type: 'shell_result', exitCode: 1, signal: null, output: '', error: 'boom', truncated: false },
    })
    if (call?.kind !== 'execute' || !call.result || !('commands' in call.result))
      throw new Error('a shell call is an execute row')
    expect(call.result.commands).toEqual([{ output: 'boom', exitCode: 1, truncated: false }])
  })

  it('draws a signal that ended a command', () => {
    const call = finishedCall('run_shell_command', 'execute', { command: 'sleep 9' }, {
      content: acpTextContent('Command: sleep 9'),
      rawOutput: { type: 'shell_result', exitCode: null, signal: 'SIGTERM', output: 'partial', error: null },
    })
    expect(call?.kind === 'execute' && call.result && 'commands' in call.result && call.result.commands[0]).toMatchObject({ signal: 'SIGTERM', output: 'partial' })
  })

  it('draws a foreground subagent with its report and summary', () => {
    const { container } = renderACPToolPair(AgentProvider.QWEN_CODE, { title: 'Agent', kind: 'other', rawInput: { description: 'Child probe', prompt: 'List', subagent_type: 'general-purpose' }, _meta: { toolName: 'agent' } }, {
      content: acpTextContent('Child done: listed.'),
      _meta: { toolName: 'agent' },
      rawOutput: { type: 'task_execution', subagentName: 'general-purpose', status: 'completed', terminateReason: 'GOAL', result: '**Listed** it.', executionSummary: { rounds: 2, totalDurationMs: 2100, totalToolCalls: 1, totalTokens: 220 } },
    })
    expect(container.textContent).toContain('Agent "Child probe" completed')
    expect(container.textContent).toContain('Tool calls:1')
    expect(container.textContent).toContain('Duration:2.1s')
    expect(container.textContent).not.toContain('Ended by')
    expect(container.querySelector('strong')?.textContent).toBe('Listed')
  })

  it('states why a subagent ended when it did not reach its goal', () => {
    const call = finishedCall('agent', 'other', { description: 'Child', prompt: 'p' }, {
      content: acpTextContent('stopped'),
      rawOutput: { type: 'task_execution', status: 'completed', terminateReason: 'MAX_TURNS', result: 'partial' },
    })
    expect(call?.kind === 'agent' && call.result && 'agents' in call.result && call.result.agents[0]?.metadata).toContainEqual({ label: 'Ended by', value: 'MAX_TURNS' })
  })

  it('draws a background subagent as running, with the id the model uses', () => {
    const call = finishedCall('agent', 'other', { description: 'Background child', prompt: 'p', run_in_background: true }, {
      content: acpTextContent('Background agent launched successfully.\ntask_id: general-purpose-call_1 (internal ID)\nThe agent is working in the background.'),
      rawOutput: { type: 'task_execution', subagentName: 'general-purpose', executionMode: 'background', status: 'background' },
    })
    if (call?.kind !== 'agent' || !call.result || !('agents' in call.result))
      throw new Error('a subagent launch is an agent row')
    expect(call.request).toMatchObject({ description: 'Background child', registryKey: 'call' })
    expect(call.result.agents[0]).toMatchObject({ agentId: 'general-purpose-call_1', outcome: 'running', body: '' })
  })

  it('draws a subagent run that failed', () => {
    const call = finishedCall('agent', 'other', { description: 'Child', prompt: 'p' }, {
      content: acpTextContent('ok'),
      rawOutput: { type: 'task_execution', status: 'failed', result: 'crashed' },
    })
    expect(call?.kind === 'agent' && call.result && 'agents' in call.result && call.result.agents[0]?.outcome).toBe('failed')
  })

  it('draws a workflow run with its id and result', () => {
    const call = finishedCall('workflow', 'other', { scriptPath: '/w/review.js', args: { depth: 2 } }, {
      content: acpTextContent('done\n--- workflow run ---\nrunId: wf_1'),
      rawOutput: '```json\n{"runId":"wf_1","phases":["a","b"],"result":{"ok":true},"tokens":{"spent":5}}\n```',
    })
    if (call?.kind !== 'agent' || !call.result || !('agents' in call.result))
      throw new Error('a workflow is an agent row')
    expect(call.request.metadata).toEqual([{ label: 'Script', value: '/w/review.js' }, { label: 'Arguments', value: '{"depth":2}' }])
    expect(call.result.agents[0]).toMatchObject({ agentId: 'wf_1', outcome: 'completed', body: '{\n  "ok": true\n}' })
    expect(call.result.agents[0]?.metadata).toContainEqual({ label: 'Phases', value: 'a, b' })
  })

  it('draws a workflow whose record is not JSON with its words', () => {
    const call = finishedCall('workflow', 'other', { script: 'x' }, { content: acpTextContent('words only'), rawOutput: 'words only' })
    expect(call?.kind === 'agent' && call.result && 'agents' in call.result && call.result.agents[0]).toMatchObject({ agentId: '', body: 'words only' })
  })

  it('draws the answers of a question from Qwen\'s record', () => {
    const call = finishedCall('ask_user_question', 'think', { questions: [{ question: 'Color?', header: 'Color', options: [{ label: 'Blue' }], multiSelect: false }] }, {
      content: acpTextContent('User has provided the following answers:\n\n**Color**: Blue'),
      rawOutput: { type: 'ask_user_question_answers', answers: [{ question: 'Color?', answer: 'Blue' }] },
    })
    expect(call?.kind === 'question' && call.result).toEqual({ answers: [{ header: 'Color?', answer: 'Blue' }] })
  })

  it('draws a notebook edit as a change to the notebook', () => {
    const call = finishedCall('notebook_edit', 'edit', { notebook_path: '/p/a.ipynb', new_source: 'x = 1' }, { content: [] })
    expect(call?.kind === 'edit' && call.request.changes[0]?.filePath).toBe('/p/a.ipynb')
  })

  it('states the action of each cron tool', () => {
    expect(finishedCall('cron_create', 'other', { cron: '*/5 * * * *', prompt: 'Check' }, { content: acpTextContent('ok') })?.kind === 'trigger').toBe(true)
    const call = finishedCall('loop_wakeup', 'other', { delaySeconds: 60, prompt: 'Look again' }, { content: acpTextContent('ok') })
    expect(call?.kind === 'trigger' && call.request).toEqual({ action: 'create', name: 'Look again', schedule: 'in 60s' })
    const stop = finishedCall('task_stop', 'other', { task_id: 't-1' }, { content: acpTextContent('Stopped.') })
    expect(stop?.kind === 'task' && stop.request).toEqual({ action: 'stop', taskId: 't-1' })
  })

  describe('the question row', () => {
    const QUESTION = { questions: [{ question: 'Color?', header: 'Color', options: [{ label: 'Blue' }] }] }

    it('draws the reason of a question that failed', () => {
      const call = finishedCall('ask_user_question', 'think', QUESTION, { status: 'failed', content: acpTextContent('The dialog closed.') })
      expect(call?.kind === 'question' && call.result).toEqual(failedResult('The dialog closed.'))
    })

    // An answer with no record of Qwen's still reaches the row, as the words of
    // the call under the question's title.
    it('draws the words of a question that states no answer record', () => {
      const call = finishedCall('ask_user_question', 'think', QUESTION, { content: acpTextContent('Blue') })
      expect(call?.kind === 'question' && call.result).toEqual({ answers: [{ header: 'Color', answer: 'Blue' }] })
    })

    it('titles the row by the header, else the question, else a generic word', () => {
      expect(openCall('ask_user_question', 'think', QUESTION)?.title).toBe('Color')
      expect(openCall('ask_user_question', 'think', { questions: [{ question: 'Color?', options: [] }] })?.title).toBe('Color?')
      expect(openCall('ask_user_question', 'think', {})?.title).toBe('Question')
    })

    it('draws no answer while the dialog is open', () => {
      const call = openCall('ask_user_question', 'think', QUESTION)
      expect(call?.kind).toBe('question')
      expect(call?.result).toBeUndefined()
    })
  })

  describe('the to-do row', () => {
    const TODOS = { todos: [{ id: '1', content: 'Inspect code', status: 'in_progress' }] }

    it('draws the reason of a list that failed, and no list while the call runs', () => {
      const failed = finishedCall('todo_write', 'think', TODOS, { status: 'failed', content: acpTextContent('Bad list.') })
      expect(failed?.kind === 'todo' && failed.result).toEqual(failedResult('Bad list.'))
      const running = openCall('todo_write', 'think', TODOS)
      expect(running?.kind === 'todo' && running.request.items.map(item => item.content)).toEqual(['Inspect code'])
      expect(running?.result).toBeUndefined()
    })
  })

  describe('the shell row', () => {
    it('joins the output and the error, and reads a numeric signal', () => {
      const call = finishedCall('run_shell_command', 'execute', { command: 'kill' }, {
        content: acpTextContent('Command: kill'),
        rawOutput: { type: 'shell_result', exitCode: null, signal: 9, output: 'out', error: 'err', truncated: true },
      })
      expect(call?.kind === 'execute' && call.result && 'commands' in call.result && call.result.commands).toEqual([{ output: 'out\nerr', signal: '9', truncated: true }])
    })

    it('states no exit for a record that states neither a code nor a signal', () => {
      const call = finishedCall('run_shell_command', 'execute', { command: 'x' }, {
        content: acpTextContent('Command: x'),
        rawOutput: { type: 'shell_result', output: 'done' },
      })
      expect(call?.kind === 'execute' && call.result && 'commands' in call.result && call.result.commands).toEqual([{ output: 'done', truncated: false }])
    })

    // A failed call keeps the shared failure body: the record of a command that
    // Qwen did not run to an end is not the answer.
    it('keeps the shared body of a command that failed', () => {
      const call = finishedCall('run_shell_command', 'execute', { command: 'x' }, {
        status: 'failed',
        content: acpTextContent('The command was refused.'),
        rawOutput: { type: 'shell_result', exitCode: 0, output: 'from the record' },
      })
      expect(JSON.stringify(call?.result)).not.toContain('from the record')
      expect(JSON.stringify(call?.result)).toContain('The command was refused.')
    })
  })

  describe('a Model Context Protocol tool', () => {
    it('draws the blocks of a call that answered', () => {
      const call = finishedCall('mcp__docs__search', 'other', { q: 'x' }, { content: acpTextContent('hit') })
      expect(call?.kind === 'mcp' && call.request).toMatchObject({ server: 'docs', tool: 'search' })
      expect(call?.kind === 'mcp' && call.result).toMatchObject({ content: [{ type: 'text', text: 'hit' }] })
    })

    it('draws the reason of a call that failed, and no answer while the call runs', () => {
      const failed = finishedCall('mcp__docs__search', 'other', { q: 'x' }, { status: 'failed', content: acpTextContent('server down') })
      expect(failed?.result).toEqual(failedResult('server down'))
      expect(openCall('mcp__docs__search', 'other', { q: 'x' })?.result).toBeUndefined()
    })
  })

  describe('the scheduled job rows', () => {
    it('states the action and the target of each cron tool', () => {
      expect(finishedCall('cron_delete', 'other', { id: 'cron-1' }, { content: acpTextContent('Deleted.') })?.request).toEqual({ action: 'delete', triggerId: 'cron-1' })
      expect(finishedCall('cron_list', 'other', {}, { content: acpTextContent('None.') })?.request).toEqual({ action: 'list' })
      expect(finishedCall('cron_create', 'other', { cron: '*/5 * * * *', prompt: 'Check' }, { content: acpTextContent('ok') })?.request).toEqual({ action: 'create', name: 'Check', schedule: '*/5 * * * *' })
    })

    it('draws the words the tool wrote as the answer', () => {
      const call = finishedCall('cron_delete', 'other', { id: 'cron-1' }, { content: acpTextContent('Deleted job cron-1.') })
      expect(call?.result).toEqual(proseResult('Deleted job cron-1.'))
    })
  })

  it('draws the answer of a skill as markdown', () => {
    const call = finishedCall('skill', 'other', { skill: 'review' }, { content: acpTextContent('# Review\n\nLoaded.') })
    expect(call?.kind === 'skill' && call.result).toEqual(proseResult('# Review\n\nLoaded.', 'markdown'))
  })

  it('draws the words of a web search as its summary', () => {
    const call = finishedCall('web_search', 'search', { query: 'leapmux' }, { content: acpTextContent('1. LeapMux') })
    expect(call?.kind === 'web_search' && call.result).toEqual({ links: [], summary: '1. LeapMux' })
  })

  describe('the read rows', () => {
    it('numbers a read from the offset of its arguments', () => {
      const call = finishedCall('read_file', 'read', { file_path: '/p/a.ts', offset: 9 }, { content: acpTextContent('ten\n') })
      expect(call?.kind === 'read' && call.result && 'lines' in call.result && call.result.lines).toEqual([{ num: 10, text: 'ten' }])
    })

    // A picture is the answer of an image file and of a zoom, and it rides the
    // call's images, so the row claims no lines of text.
    it('claims no lines for a read that answered with a picture', () => {
      const image = finishedCall('read_file', 'read', { file_path: '/p/shot.png' }, { content: [IMAGE_BLOCK] })
      expect(image?.kind === 'read' && image.result).toEqual({ lines: null, fallbackContent: '' })
      const zoom = finishedCall('zoom_image', 'read', { file_path: '/p/shot.png' }, { content: [...acpTextContent('The region.'), IMAGE_BLOCK] })
      expect(zoom?.kind === 'read' && zoom.result).toEqual({ lines: null, fallbackContent: 'The region.' })
      expect(zoom?.images?.length).toBe(1)
    })

    it('draws the reason of a read that failed', () => {
      const call = finishedCall('read_file', 'read', { file_path: '/p/a.ts' }, { status: 'failed', content: acpTextContent('No such file.') })
      expect(call?.result).toEqual(failedResult('No such file.'))
    })
  })

  // Only text that matches Qwen's own layout parses. Any other text keeps the
  // shared answer, which is the words Qwen printed.
  it('keeps the shared answer of a search or a listing whose text is not Qwen\'s layout', () => {
    for (const [name, kind] of [['grep_search', 'search'], ['glob', 'search'], ['list_directory', 'search']] as const) {
      const call = finishedCall(name, kind, { pattern: 'x', path: '/p' }, { content: acpTextContent('Something went sideways.') })
      expect(call?.kind, name).not.toBe('mcp')
      expect(JSON.stringify(call?.result), name).toContain('Something went sideways.')
    }
  })

  describe('the task stop row', () => {
    it('states the stop while it runs, the reason of a stop that failed, and the words of one that ended', () => {
      const running = openCall('task_stop', 'other', { task_id: 't-1' })
      expect(running?.kind === 'task' && running.request).toEqual({ action: 'stop', taskId: 't-1' })
      expect(running?.result).toBeUndefined()
      expect(finishedCall('task_stop', 'other', { task_id: 't-1' }, { status: 'failed', content: acpTextContent('No such task.') })?.result).toEqual(failedResult('No such task.'))
      expect(finishedCall('task_stop', 'other', { task_id: 't-1' }, { content: acpTextContent('Stopped.') })?.result).toEqual({ outcome: 'stopped', output: 'Stopped.' })
    })
  })

  describe('the mode rows', () => {
    it('titles the plan tools by what they do', () => {
      expect(openCall('exit_plan_mode', 'switch_mode', { plan: '1. X' })?.title).toBe('Exit plan mode')
      expect(openCall('enter_plan_mode', 'switch_mode', {})?.title).toBe('Enter plan mode')
    })

    it('states the worktree mode and its target, and no target when the call gives none', () => {
      expect(openCall('enter_worktree', 'other', { name: 'feature' })?.request).toMatchObject({ mode: 'worktree', target: 'feature' })
      const leave = openCall('exit_worktree', 'other', {})
      expect(leave?.request).toMatchObject({ mode: 'leave worktree' })
      expect(leave?.request).not.toHaveProperty('target')
    })
  })
})
