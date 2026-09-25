import { describe, expect, it } from 'vitest'
import { KIMI_TOOL } from '~/generated/contracts/kimi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { input } from '../../testUtils'
import { kimiAgentRuns, kimiFileChanges, kimiOutput, kimiSwarmRuns, kimiToolRow } from './toolCall'
import '../../index'

const CALL = 'call_1'

/** The call a result frame extracts, with its start beside it. */
function resultCall(name: string, args: Record<string, unknown>, output: unknown, extra: Record<string, unknown> = {}, display?: Record<string, unknown>) {
  return providerToolCall(AgentProvider.KIMI_CODE, kimiToolResult(CALL, output, extra), {
    request: input(kimiToolStart(CALL, name, args, display), undefined, AgentProvider.KIMI_CODE),
    spanType: name,
  })
}

describe('kimiToolRow', () => {
  it('reads a start as a request of its call', () => {
    const row = kimiToolRow(kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' }, { kind: 'command' }), undefined, undefined, undefined, undefined)
    expect(row).toMatchObject({ toolCallId: CALL, toolName: KIMI_TOOL.Bash, args: { command: 'ls' }, display: { kind: 'command' }, result: null, finished: false })
  })

  it('reads a result with the name and the arguments of its start', () => {
    const row = kimiToolRow(kimiToolResult(CALL, 'a.go'), KIMI_TOOL.Glob, kimiToolStart(CALL, KIMI_TOOL.Glob, { pattern: '*' }), undefined, undefined)
    expect(row).toMatchObject({ toolName: KIMI_TOOL.Glob, args: { pattern: '*' }, finished: true })
    expect(row?.lifecycle.resultFrameLanded).toBe(true)
  })

  it('reads a result whose start is out of the window by its span type', () => {
    const row = kimiToolRow(kimiToolResult(CALL, 'ok'), KIMI_TOOL.Read, undefined, undefined, undefined)
    expect(row).toMatchObject({ toolCallId: CALL, toolName: KIMI_TOOL.Read, args: {}, display: undefined, finished: true, retained: false })
  })

  it('reads a result that states no tool and has no span type with no name', () => {
    expect(kimiToolRow(kimiToolResult(CALL, 'ok'), undefined, undefined, undefined, undefined)?.toolName).toBe('')
  })

  it('reads a start with no completion, or an unstated one, as a request', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' })
    for (const completion of [undefined, MessageCompletion.UNSPECIFIED]) {
      const row = kimiToolRow(start, undefined, undefined, undefined, completion)
      expect(row, String(completion)).toMatchObject({ finished: false, retained: false, result: null })
      expect(row?.lifecycle, String(completion)).toMatchObject({ retainedOutcome: null, rowFinal: false, resultFrameLanded: false })
    }
  })

  it('states the failed outcome of a result frame that reports an error', () => {
    const row = kimiToolRow(kimiToolResult(CALL, 'boom', { isError: true }), KIMI_TOOL.Bash, undefined, undefined, undefined)
    expect(row?.lifecycle).toMatchObject({ providerOutcome: 'failed', rowFinal: true, resultFrameLanded: true })
    expect(kimiToolRow(kimiToolResult(CALL, 'ok'), KIMI_TOOL.Bash, undefined, undefined, undefined)?.lifecycle.providerOutcome).toBeNull()
  })

  it('reads the outcome of each way a turn can end around a retained start', () => {
    const start = kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'sleep 9' })
    const completed = providerToolCall(AgentProvider.KIMI_CODE, start, { spanType: KIMI_TOOL.Bash, completion: MessageCompletion.COMPLETE })
    expect(kimiToolRow(start, undefined, undefined, undefined, MessageCompletion.COMPLETE)?.lifecycle.retainedOutcome).toBe('succeeded')
    // The turn ended although the call printed nothing, so the call answers nothing.
    expect(completed?.status).toBe('incomplete')
    expect(completed?.result).toBeUndefined()
    expect(kimiToolRow(start, undefined, undefined, undefined, MessageCompletion.ERROR)?.lifecycle.retainedOutcome).toBe('failed')
    expect(providerToolCall(AgentProvider.KIMI_CODE, start, { spanType: KIMI_TOOL.Bash, completion: MessageCompletion.ERROR })?.status).toBe('failed')
  })

  it('reads the content parts a retained start carries as its result', () => {
    const start = { ...kimiToolStart(CALL, KIMI_TOOL.Read, { path: 'a.go' }), output: [{ type: 'text', text: '1\tpackage a' }] }
    const row = kimiToolRow(start, undefined, undefined, undefined, MessageCompletion.ERROR)
    expect(row?.result).toBe(start)
    expect(row?.lifecycle.resultFrameLanded).toBe(true)
    const call = providerToolCall(AgentProvider.KIMI_CODE, start, { spanType: KIMI_TOOL.Read, completion: MessageCompletion.ERROR })
    expect(call?.status).toBe('failed')
    expect(call?.result).toMatchObject({ lines: [{ num: 1, text: 'package a' }] })
  })

  it('reads no output from a retained start whose output is neither text nor parts', () => {
    for (const output of [42, { text: 'x' }, null]) {
      const start = { ...kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'x' }), output }
      expect(kimiToolRow(start, undefined, undefined, undefined, MessageCompletion.INTERRUPTED)?.result, JSON.stringify(output)).toBeNull()
    }
  })

  it('reads a retained start as the finished call, with how its turn ended', () => {
    const row = kimiToolRow(kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'sleep 9' }), undefined, undefined, kimiToolResult(CALL, 'late'), MessageCompletion.INTERRUPTED)
    expect(row?.finished).toBe(true)
    expect(row?.retained).toBe(true)
    expect(row?.result).toBeNull()
    expect(row?.lifecycle.retainedOutcome).toBe('interrupted')
  })

  it('reads the output a retained start carries as its result', () => {
    const start = { ...kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'make' }), output: 'building...\n' }
    const call = providerToolCall(AgentProvider.KIMI_CODE, start, { spanType: KIMI_TOOL.Bash, completion: MessageCompletion.INTERRUPTED })
    expect(call?.status).toBe('cancelled')
    expect(call?.result).toStrictEqual({ commands: [{ output: 'building...\n' }], unresolvedTerminals: [] })
  })

  it('reads no row that is not a tool event', () => {
    expect(kimiToolRow({ type: 'turn.ended' }, undefined, undefined, undefined, undefined)).toBeNull()
  })
})

describe('kimi execute calls', () => {
  it('reads a successful command with its exit code of zero', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'echo hi', description: 'Say hi' }, 'hi\n', {}, { kind: 'command', command: 'echo hi', cwd: '/work', language: 'bash' })
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('completed')
    expect(call?.request).toStrictEqual({ command: 'echo hi', description: 'Say hi', cwd: '/work', language: 'bash' })
    expect(call?.result).toStrictEqual({ commands: [{ output: 'hi\n', exitCode: 0 }], unresolvedTerminals: [] })
  })

  it('reads the exit code a failed command states and drops the trailer', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'false' }, 'oops\nCommand failed with exit code: 2.', { isError: true })
    expect(call?.status).toBe('failed')
    expect(call?.result).toStrictEqual({ commands: [{ output: 'oops', exitCode: 2 }], unresolvedTerminals: [] })
  })

  it('reads a command the server stopped as cancelled', () => {
    for (const trailer of ['Command killed by timeout (60s)', 'Interrupted by user']) {
      const call = resultCall(KIMI_TOOL.Bash, { command: 'sleep 99' }, `partial\n${trailer}`, { isError: true })
      expect(call?.status, trailer).toBe('cancelled')
    }
  })

  it('states no result while the command runs', () => {
    const call = providerToolCall(AgentProvider.KIMI_CODE, kimiToolStart(CALL, KIMI_TOOL.Bash, { command: 'ls' }), { spanType: KIMI_TOOL.Bash, role: 'request' })
    expect(call?.result).toBeUndefined()
  })

  it('ignores a language the command body cannot highlight', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, 'ok', {}, { kind: 'command', language: 'cobol' })
    expect(call?.request).toStrictEqual({ command: 'x' })
  })

  it('reads the command from the display when the arguments state none, and the argument directory first', () => {
    const call = resultCall(KIMI_TOOL.Bash, { cwd: '/args' }, 'ok', {}, { kind: 'command', command: 'ls', cwd: '/display' })
    expect(call?.request).toStrictEqual({ command: 'ls', cwd: '/args' })
  })

  it('reads a negative exit code from the trailer', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'kill -9 $$' }, 'Command failed with exit code: -1.', { isError: true })
    expect(call?.result).toStrictEqual({ commands: [{ output: '', exitCode: -1 }], unresolvedTerminals: [] })
  })

  it('reads the trailer only at the end of the output', () => {
    const output = 'Command failed with exit code: 2.\nmore output'
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, output, { isError: true })
    // A failed command whose output states no trailer states no exit code.
    expect(call?.status).toBe('failed')
    expect(call?.result).toStrictEqual({ commands: [{ output }], unresolvedTerminals: [] })
  })

  it('reads a stop notice in the output of a command that succeeded as plain output', () => {
    const call = resultCall(KIMI_TOOL.Bash, { command: 'x' }, 'out\nInterrupted by user')
    expect(call?.status).toBe('completed')
    expect(call?.result).toStrictEqual({ commands: [{ output: 'out\nInterrupted by user', exitCode: 0 }], unresolvedTerminals: [] })
  })
})

describe('kimi file calls', () => {
  it('reads a read with its numbered lines and its system note', () => {
    const call = resultCall(KIMI_TOOL.Read, { path: 'a.go', line_offset: 3, n_lines: 2 }, '3\tpackage a\n4\tfunc A() {}\n<system>2 lines read from file.</system>', {}, { kind: 'file_io', path: '/work/a.go' })
    expect(call?.request).toStrictEqual({ path: '/work/a.go', offset: 3, limit: 2 })
    expect(call?.result).toMatchObject({ lines: [{ num: 3, text: 'package a' }, { num: 4, text: 'func A() {}' }] })
  })

  it('reads a write and an edit as the change they asked for', () => {
    const write = resultCall(KIMI_TOOL.Write, { path: 'goal.txt', content: 'ok\n' }, 'Wrote 3 bytes', {}, { kind: 'file_io', path: '/work/goal.txt' })
    expect(write?.result).toStrictEqual({ changes: [{ filePath: '/work/goal.txt', operation: 'add', oldStr: '', newStr: 'ok\n', structuredPatch: null }] })
    const edit = resultCall(KIMI_TOOL.Edit, { path: 'a.go', old_string: 'x', new_string: 'y', replace_all: true }, 'Edited')
    expect(edit?.request).toStrictEqual({ changes: [{ filePath: 'a.go', operation: 'edit', oldStr: 'x', newStr: 'y', structuredPatch: null }], replaceAll: true })
  })

  it('reads a read with no window, and ignores a window that is not a number', () => {
    expect(resultCall(KIMI_TOOL.Read, { path: 'a.go' }, '1\tx')?.request).toStrictEqual({ path: 'a.go' })
    expect(resultCall(KIMI_TOOL.Read, { path: 'a.go', line_offset: '3', n_lines: null }, '1\tx')?.request).toStrictEqual({ path: 'a.go' })
  })

  it('reads a media read as the picture it returned', () => {
    const call = resultCall(KIMI_TOOL.ReadMediaFile, { path: 'a.png' }, [{ type: 'text', text: 'Read image a.png' }, { type: 'image_url', imageUrl: { url: 'data:image/png;base64,QUJD' } }])
    expect(call?.kind).toBe('read')
    expect(call?.result).toStrictEqual({ lines: null, fallbackContent: 'Read image a.png' })
    expect(call?.images).toStrictEqual([{ mimeType: 'image/png', data: 'QUJD' }])
  })

  it('reads a file change that states no file as the generic card', () => {
    const call = resultCall(KIMI_TOOL.Write, { content: 'x' }, 'Wrote')
    expect(call?.kind).toBe('other')
    expect(resultCall(KIMI_TOOL.Edit, { old_string: 'x', new_string: 'y' }, 'Edited')?.kind).toBe('other')
  })

  it('states the change of each kind', () => {
    expect(kimiFileChanges('write', {})).toStrictEqual([])
    expect(kimiFileChanges('edit', { path: 'a' })).toStrictEqual([{ filePath: 'a', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null }])
  })
})

describe('kimi search calls', () => {
  it('reads a glob as its paths', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.go' }, 'a.go\nb.go\n')
    expect(call?.result).toMatchObject({ filenames: ['a.go', 'b.go'], numFiles: 2, empty: false })
  })

  it('reads a glob that found nothing as empty', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.rs' }, 'No files found.')
    expect(call?.result).toMatchObject({ filenames: [], numFiles: 0, empty: true })
  })

  it('reads the grep output of each mode', () => {
    const content = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, 'a.go:3:x\nb.go:9:x')
    expect(content?.result).toMatchObject({ filenames: ['a.go', 'b.go'], numLines: 2, mode: 'content', empty: false })
    const files = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'files_with_matches' }, 'a.go')
    expect(files?.result).toMatchObject({ filenames: ['a.go'], mode: 'files_with_matches' })
    const count = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'count_matches' }, 'a.go:2')
    expect(count?.result).toMatchObject({ mode: 'count' })
    const none = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, 'No matches found.')
    expect(none?.result).toMatchObject({ empty: true, numLines: 0 })
  })

  // A `<system>` note is the server's own words, not a match, and a line with no
  // `path:` prefix names no file.
  it('counts no system note and no file for a line that states none', () => {
    const output = 'a.go:1:x\nnot a match line\n<system>Output truncated.</system>'
    const call = resultCall(KIMI_TOOL.Grep, { pattern: 'x' }, output, { truncated: true })
    expect(call?.result).toStrictEqual({
      filenames: ['a.go'],
      content: 'a.go:1:x\nnot a match line',
      numFiles: 1,
      numLines: 2,
      truncated: true,
      fallbackContent: output,
      empty: false,
      mode: 'content',
    })
  })

  it('reads a file-list grep that found nothing as empty', () => {
    const call = resultCall(KIMI_TOOL.Grep, { pattern: 'x', output_mode: 'files_with_matches' }, 'No files found.')
    expect(call?.result).toMatchObject({ filenames: [], numFiles: 0, empty: true, mode: 'files_with_matches' })
  })

  it('reads a glob whose output is only a system note as empty', () => {
    const call = resultCall(KIMI_TOOL.Glob, { pattern: '*.rs' }, '<system>No files matched.</system>\n')
    expect(call?.result).toMatchObject({ filenames: [], numFiles: 0, empty: true })
  })
})

describe('kimi fetch and web search calls', () => {
  it('reads a fetch as the page it returned', () => {
    const call = resultCall(KIMI_TOOL.FetchURL, { url: 'https://example.com' }, '# Example')
    expect(call?.result).toStrictEqual({ result: '# Example' })
  })

  it('reads a web search as its summary, with no links', () => {
    const call = resultCall(KIMI_TOOL.WebSearch, { query: 'kimi' }, 'Kimi Code is a coding agent.')
    expect(call?.request).toStrictEqual({ query: 'kimi' })
    expect(call?.result).toStrictEqual({ links: [], summary: 'Kimi Code is a coding agent.' })
  })
})

describe('kimi subagent calls', () => {
  it('reads an Agent result as its run', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe', subagent_type: 'explore' }, 'agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\n\n[summary]\nDone.\n\nresume_hint: Continue.')
    expect(call?.request).toStrictEqual({ description: 'Probe', agentType: 'explore', prompt: 'Look.' })
    expect(call?.result).toStrictEqual({ agents: [{ description: 'Probe', agentId: 'agent-0', outcome: 'completed', metadata: [{ label: 'Type', value: 'explore' }], body: 'Done.' }] })
  })

  it('reads a swarm result as one run for each member', () => {
    const runs = kimiSwarmRuns('<agent_swarm_result>\n<subagent agent_id="agent-0" item="alpha" outcome="completed">a</subagent>\n<subagent agent_id="agent-1" item="beta" outcome="failed">b</subagent>\n<subagent agent_id="agent-2" outcome="suspended">c</subagent>\n</agent_swarm_result>')
    expect(runs.map(run => [run.agentId, run.description, run.outcome, run.statusLabel])).toStrictEqual([
      ['agent-0', 'alpha', 'completed', undefined],
      ['agent-1', 'beta', 'failed', undefined],
      ['agent-2', 'agent-2', 'unknown', 'suspended'],
    ])
  })

  it('reads the outcome words of a run', () => {
    for (const [status, outcome] of [['failed', 'failed'], ['timed_out', 'failed'], ['cancelled', 'stopped'], ['killed', 'stopped'], ['running', 'running'], ['odd', 'unknown']] as const)
      expect(kimiAgentRuns(`agent_id: a\nstatus: ${status}\n\n[summary]\nx`, 'd')[0]?.outcome, status).toBe(outcome)
    expect(kimiAgentRuns('no header here', 'd')).toStrictEqual([])
  })

  it('keeps an Agent result it cannot read as the words', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe' }, 'The subagent is running in the background.')
    expect(call?.result).toStrictEqual({ unparsed: true, text: 'The subagent is running in the background.' })
  })

  it('states no result for an Agent result that states nothing', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.', description: 'Probe' }, '')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  // A swarm states a template and items rather than one prompt.
  it('reads a swarm request by its description and its template', () => {
    const call = resultCall(KIMI_TOOL.AgentSwarm, { description: 'Probe swarm', prompt_template: 'Reply with {{item}}.', items: ['alpha'] }, '<subagent agent_id="agent-0" item="alpha" outcome="completed">alpha done</subagent>')
    expect(call?.request).toStrictEqual({ description: 'Probe swarm', prompt: 'Reply with {{item}}.' })
    expect(call?.title).toBe('Probe swarm')
    expect(call?.result).toStrictEqual({ agents: [{ description: 'alpha', agentId: 'agent-0', outcome: 'completed', metadata: [], body: 'alpha done' }] })
  })

  it('reads the agent name the display states when the call states no description', () => {
    const call = resultCall(KIMI_TOOL.Agent, { prompt: 'Look.' }, 'agent_id: agent-3\nstatus: running', {}, { agent_name: 'Explorer' })
    expect(call?.request).toStrictEqual({ description: 'Explorer', prompt: 'Look.' })
    expect(call?.result).toStrictEqual({ agents: [{ description: 'Explorer', agentId: 'agent-3', outcome: 'running', metadata: [], body: '' }] })
  })

  it('reads no swarm run from a result with no subagent element', () => {
    expect(kimiSwarmRuns('')).toStrictEqual([])
    expect(kimiSwarmRuns('<agent_swarm_result><summary>completed: 0</summary></agent_swarm_result>')).toStrictEqual([])
  })
})

describe('kimi question and to-do calls', () => {
  it('reads the answers an AskUserQuestion result states', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [{ question: 'Which?', header: 'Pick', options: [{ label: 'A' }, { label: 'B' }] }] }, '{"answers":{"Which?":"A"}}')
    expect(call?.request).toStrictEqual({ questions: [{ header: 'Pick', question: 'Which?', options: [{ label: 'A' }, { label: 'B' }] }] })
    expect(call?.result).toStrictEqual({ answers: [{ header: 'Which?', answer: 'A' }] })
  })

  it('reads a dismissed question as no answers', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{},"note":"User dismissed the question without answering."}')
    expect(call?.result).toStrictEqual({ answers: [] })
  })

  it('keeps a question result it cannot read as the words', () => {
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, 'not json')?.result).toStrictEqual({ unparsed: true, text: 'not json' })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"note":"no answers"}')?.result).toStrictEqual({ unparsed: true, text: '{"note":"no answers"}' })
    expect(resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '["Which?"]')?.result).toStrictEqual({ unparsed: true, text: '["Which?"]' })
  })

  it('reads an answer that is not a string as no answer', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '{"answers":{"Which?":["A","B"]}}')
    expect(call?.result).toStrictEqual({ answers: [{ header: 'Which?', answer: null }] })
  })

  it('states no result for a question result that states nothing', () => {
    const call = resultCall(KIMI_TOOL.AskUserQuestion, { questions: [] }, '')
    expect(call?.status).toBe('incomplete')
    expect(call?.result).toBeUndefined()
  })

  it('reads a to-do list call as its items', () => {
    const call = resultCall(KIMI_TOOL.TodoList, { todos: [{ title: 'A', status: 'done' }, { title: 'B', status: 'pending' }] }, 'Todo list updated.')
    expect(call?.kind).toBe('todo')
    expect(call?.request).toMatchObject({ items: [{ content: 'A', status: 'completed' }, { content: 'B', status: 'pending' }] })
    // Each call states the whole list, so the result is the list the call wrote.
    expect(call?.result).toStrictEqual({ items: (call?.request as { items: unknown[] }).items })
  })

  it('reads an empty to-do list as a list that holds nothing', () => {
    const call = resultCall(KIMI_TOOL.TodoList, { todos: [] }, 'Todo list updated.')
    expect(call?.kind).toBe('todo')
    expect(call?.request).toStrictEqual({ items: [] })
  })

  it('reads a to-do call that only reads the list as the generic card', () => {
    expect(resultCall(KIMI_TOOL.TodoList, {}, 'Current todo list: (empty)')?.kind).toBe('other')
  })
})

describe('kimi task, trigger and wait calls', () => {
  it('reads the action each task tool states', () => {
    expect(resultCall(KIMI_TOOL.TaskOutput, { task_id: 'bash-1' }, 'x')?.request).toStrictEqual({ action: 'output', taskId: 'bash-1' })
    expect(resultCall(KIMI_TOOL.TaskStop, { task_id: 'bash-1' }, 'x')?.request).toStrictEqual({ action: 'stop', taskId: 'bash-1' })
    expect(resultCall(KIMI_TOOL.TaskList, {}, 'x')?.request).toStrictEqual({ action: 'list' })
  })

  it('reads the action and the schedule of a cron call', () => {
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *', prompt: 'Check.' }, 'ok')?.request).toStrictEqual({ action: 'create', schedule: '0 9 * * *', name: 'Check.' })
    expect(resultCall(KIMI_TOOL.CronDelete, { id: 'cron-1' }, 'ok')?.request).toStrictEqual({ action: 'delete', triggerId: 'cron-1' })
  })

  it('reads a wait limit in seconds', () => {
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: 30 }, 'ok')?.request).toStrictEqual({ durationMs: 30000 })
    expect(resultCall(KIMI_TOOL.WaitFor, {}, 'ok')?.request).toStrictEqual({})
  })

  it('reads what a task tool printed as its output', () => {
    const call = resultCall(KIMI_TOOL.TaskOutput, { task_id: 'bash-1' }, 'status: running\noutput: hi')
    expect(call?.title).toBe(KIMI_TOOL.TaskOutput)
    expect(call?.result).toStrictEqual({ outcome: 'completed', output: 'status: running\noutput: hi' })
  })

  it('reads the list action of a cron call, and a stated name before the prompt', () => {
    expect(resultCall(KIMI_TOOL.CronList, {}, 'none')?.request).toStrictEqual({ action: 'list' })
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *', name: 'daily', prompt: 'Check.' }, 'ok')?.request)
      .toStrictEqual({ action: 'create', schedule: '0 9 * * *', name: 'daily' })
    expect(resultCall(KIMI_TOOL.CronCreate, { cron: '0 9 * * *' }, 'ok')?.request).toStrictEqual({ action: 'create', schedule: '0 9 * * *' })
  })

  it('reads a wait limit of zero seconds as zero, and ignores a limit that is not a number', () => {
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: 0 }, 'ok')?.request).toStrictEqual({ durationMs: 0 })
    expect(resultCall(KIMI_TOOL.WaitFor, { timeout: '30' }, 'ok')?.request).toStrictEqual({})
  })

  it('reads a Model Context Protocol call by its prefixed name', () => {
    const call = resultCall('mcp__github__search_repos', { q: 'x' }, 'found')
    expect(call?.kind).toBe('mcp')
    expect(call?.request).toMatchObject({ server: 'github', tool: 'search_repos' })
  })

  it('reads the words and the pictures a Model Context Protocol call returned', () => {
    const call = resultCall('mcp__s__t', { q: 1 }, [{ type: 'text', text: 'hi' }, { type: 'image_url', imageUrl: { url: 'data:image/png;base64,QUJD' } }])
    expect(call?.request).toStrictEqual({ args: { q: 1 }, server: 's', tool: 't' })
    expect(call?.result).toStrictEqual({ content: [{ type: 'text', text: 'hi' }, { type: 'image', source: { mimeType: 'image/png', data: 'QUJD' } }] })
  })
})

describe('kimi mode, message and report calls', () => {
  it('reads the mode each half of the plan-mode switch lands in', () => {
    expect(resultCall(KIMI_TOOL.EnterPlanMode, {}, 'Entered plan mode.')?.request).toStrictEqual({ mode: 'plan' })
    // An ExitPlanMode that proposes no plan is an ordinary switch back.
    const exit = resultCall(KIMI_TOOL.ExitPlanMode, {}, 'Exited plan mode.')
    expect(exit?.kind).toBe('switch_mode')
    expect(exit?.request).toStrictEqual({ mode: 'default' })
    expect(exit?.result).toStrictEqual({ text: 'Exited plan mode.', format: 'plain' })
  })

  it('reads the text of a notice from the first field that states one', () => {
    expect(resultCall(KIMI_TOOL.NotifyUser, { message: 'M', body: 'B', title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'M' })
    expect(resultCall(KIMI_TOOL.NotifyUser, { body: 'B', title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'B' })
    expect(resultCall(KIMI_TOOL.NotifyUser, { title: 'T' }, 'ok')?.request).toStrictEqual({ text: 'T' })
  })

  it('titles a call by the display description, then the argument description, then the tool name', () => {
    expect(resultCall(KIMI_TOOL.GetGoal, { description: 'From args' }, '{}', {}, { description: 'From display' })?.title).toBe('From display')
    expect(resultCall(KIMI_TOOL.GetGoal, { description: 'From args' }, '{}')?.title).toBe('From args')
    expect(resultCall(KIMI_TOOL.GetGoal, {}, '{}')?.title).toBe(KIMI_TOOL.GetGoal)
  })

  it('reads a goal call as a report of the words it returned', () => {
    const call = resultCall(KIMI_TOOL.GetGoal, {}, '{"objective":"Ship it","status":"active"}')
    expect(call?.kind).toBe('report')
    expect(call?.result).toStrictEqual({ text: '{"objective":"Ship it","status":"active"}', format: 'plain' })
  })
})

describe('kimi uncategorized calls', () => {
  it('reads a tool no table lists as the generic card, labeled by its name', () => {
    const call = resultCall('FutureTool', { q: 1 }, 'hi')
    expect(call?.kind).toBe('other')
    expect(call?.label).toBe('FutureTool')
    expect(call?.request).toStrictEqual({ args: { q: 1 } })
    expect(call?.result).toStrictEqual({ content: [{ type: 'text', text: 'hi' }] })
  })

  it('reads an empty output of the generic card as no content', () => {
    expect(resultCall('FutureTool', {}, '')?.result).toStrictEqual({ content: [] })
  })

  it('reads a call that states no tool name as the generic card with no label', () => {
    const call = providerToolCall(AgentProvider.KIMI_CODE, kimiToolStart(CALL, '', { a: 1 }), { spanType: '', role: 'request' })
    expect(call?.kind).toBe('other')
    expect(call?.label).toBeUndefined()
    expect(call?.request).toStrictEqual({ args: { a: 1 } })
  })
})

describe('kimiOutput', () => {
  it('reads a string, content parts, and anything else', () => {
    expect(kimiOutput('hi')).toStrictEqual({ text: 'hi', images: [] })
    expect(kimiOutput([
      { type: 'text', text: 'one' },
      { type: 'image_url', imageUrl: { url: 'data:image/png;base64,QUJD' } },
      { type: 'image_url', imageUrl: { url: 'https://example.com/a.png' } },
      { type: 'video_url', videoUrl: { url: 'file:///clip.mp4' } },
      { type: 'think', think: 'hidden' },
      'not a part',
    ])).toStrictEqual({
      text: 'one\nfile:///clip.mp4',
      images: [{ mimeType: 'image/png', data: 'QUJD' }, { url: 'https://example.com/a.png' }],
    })
    expect(kimiOutput(undefined)).toStrictEqual({ text: '', images: [] })
    expect(kimiOutput(42)).toStrictEqual({ text: '', images: [] })
  })

  it('states an audio clip by its URL and skips a media part that states no URL', () => {
    expect(kimiOutput([
      { type: 'audio_url', audioUrl: { url: 'file:///clip.mp3' } },
      { type: 'audio_url', audioUrl: {} },
      { type: 'video_url' },
      { type: 'image_url', imageUrl: { url: '' } },
      { type: 'image_url' },
    ])).toStrictEqual({ text: 'file:///clip.mp3', images: [] })
  })

  it('reads an empty list of parts as no words and no pictures', () => {
    expect(kimiOutput([])).toStrictEqual({ text: '', images: [] })
  })
})
