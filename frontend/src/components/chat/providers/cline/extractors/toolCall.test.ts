import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { CLINE_REJECTION_SUFFIX } from '../protocol'
import { clineToolFinishRow, clineToolStartRow } from '../toolResults.fixtures'
import { CLINE_TOOL_REQUEST_OVERRIDES, clineErrorWords, clineResultOutcome, clineToolCall } from './toolCall'
import '~/components/chat/providers'

const PATCH = '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-old\n+new\n*** End Patch'

/** The call one finish row draws, paired with its start. */
function call(name: string, input: Record<string, unknown>, output: unknown, error?: string) {
  return providerToolCall(AgentProvider.CLINE, clineToolFinishRow(name, output, error), {
    request: { wrapper: null, topLevel: null, parentObject: clineToolStartRow(name, input), rawText: '', supplementalContent: undefined, messageMetadata: undefined },
  })
}

describe('clineResultOutcome', () => {
  it('reads a refusal by Cline\'s rejection words, and every other error as a failure', () => {
    expect(clineResultOutcome(undefined)).toBeNull()
    expect(clineResultOutcome({ id: 'c', name: 'x', output: [], error: '' })).toBeNull()
    expect(clineResultOutcome({ id: 'c', name: 'x', output: {}, error: `No. -- ${CLINE_REJECTION_SUFFIX}` })).toBe('declined')
    expect(clineResultOutcome({ id: 'c', name: 'x', output: {}, error: 'boom' })).toBe('failed')
  })
})

describe('clineErrorWords', () => {
  it('drops the words that address the model', () => {
    expect(clineErrorWords(`Use the clean target. -- ${CLINE_REJECTION_SUFFIX}`)).toBe('Use the clean target.')
    expect(clineErrorWords('boom  ')).toBe('boom')
  })

  it('drops the rejection words with no separator, and leaves nothing when they are all', () => {
    expect(clineErrorWords(`No. ${CLINE_REJECTION_SUFFIX}\n`)).toBe('No.')
    expect(clineErrorWords(CLINE_REJECTION_SUFFIX)).toBe('')
    expect(clineErrorWords('')).toBe('')
  })

  // The words close the error only at its end. The same words inside the text are the
  // reader's own.
  it('keeps the rejection words that do not close the error', () => {
    const text = `${CLINE_REJECTION_SUFFIX} Then stop.`
    expect(clineErrorWords(text)).toBe(text)
    expect(clineResultOutcome({ id: 'c', name: 'x', output: {}, error: text })).toBe('failed')
  })
})

describe('CLINE_TOOL_REQUEST_OVERRIDES', () => {
  // The whole deviation list: each entry reads a Cline argument that the shared table
  // does not spell. A new entry needs a reason at its declaration.
  it('overrides only the kinds whose arguments Cline spells its own way', () => {
    expect(Object.keys(CLINE_TOOL_REQUEST_OVERRIDES).sort()).toEqual(['agent', 'agents', 'edit', 'execute', 'fetch', 'grep', 'mcp', 'message', 'question', 'read', 'switch_mode', 'task'])
  })
})

describe('cline tool requests', () => {
  const request = (name: string, args: Record<string, unknown>) =>
    clineToolCall({ call: { id: 'call_1', name, input: args }, result: undefined, finished: false }).request

  it('reads a team message under each spelling of its teammate and its text', () => {
    expect(request('team_send_message', { to: 'researcher', message: 'Hi.' })).toEqual({ to: 'researcher', text: 'Hi.' })
    expect(request('team_send_message', { agentId: 'a', to: 'b', text: 'Hi.' })).toEqual({ to: 'a', text: 'Hi.' })
    // A broadcast states no teammate.
    expect(request('team_broadcast', { body: 'Split the work.' })).toEqual({ text: 'Split the work.' })
  })

  it('reads the teammate a team tool states, and none for a tool that states none', () => {
    expect(request('team_shutdown_teammate', { agentId: 'researcher', name: 'other' })).toEqual({ query: 'researcher' })
    expect(request('team_spawn_teammate', { name: 'builder' })).toEqual({ query: 'builder' })
    expect(request('team_status', {})).toEqual({})
  })

  it('reads the run a team task tool states, and the action of each tool', () => {
    expect(request('team_run_task', { agentId: 'researcher', taskId: 'task-1' })).toEqual({ action: 'other', taskId: 'task-1' })
    expect(request('team_task', { action: 'create' })).toEqual({ action: 'other' })
    expect(request('team_cancel_run', { runId: 'run_1', taskId: 'task-1' })).toEqual({ action: 'stop', taskId: 'run_1' })
  })

  it('reads a fetch that states no page as no address', () => {
    expect(request('fetch_web_content', {})).toEqual({ url: '' })
    expect(request('fetch_web_content', { requests: ['https://a.test', { prompt: 'x' }, { url: 'https://b.test' }] })).toEqual({ url: 'https://b.test' })
  })
})

describe('cline tool rows', () => {
  it('draws the commands of a call and one result for each', () => {
    const drawn = call('run_commands', { commands: ['echo a', 'echo b'] }, [
      { query: 'echo a', result: 'a\n', success: true },
      { query: 'echo b', result: '', error: 'Command failed', success: false },
    ])
    expect(drawn?.kind).toBe('execute')
    // The call ran each command, so one failed command is no failure of the call: its
    // error and its failure are in its own result below.
    expect(drawn?.status).toBe('completed')
    expect(drawn?.request).toEqual({ command: 'echo a\necho b', language: 'bash' })
    expect(drawn?.kind === 'execute' && 'result' in drawn ? drawn.result : null).toEqual({
      commands: [{ output: 'a\n', label: 'echo a' }, { output: 'Command failed', failed: true, label: 'echo b' }],
      unresolvedTerminals: [],
    })
  })

  it('draws a subagent with its task and its report', () => {
    const drawn = call('spawn_agent', { task: 'Find the bug.\nThen report.' }, { text: 'Found it.', finishReason: 'completed' })
    expect(drawn?.request).toMatchObject({ description: 'Find the bug.', prompt: 'Find the bug.\nThen report.', registryKey: 'call_fixture_1' })
    expect(drawn?.kind === 'agent' && 'result' in drawn ? drawn.result : null).toMatchObject({ agents: [{ outcome: 'completed', body: 'Found it.' }] })
  })

  it('draws a stored subagent report, which Cline keeps as JSON text', () => {
    const drawn = call('spawn_agent', { task: 'Look.' }, JSON.stringify({ text: 'Stored.', finishReason: 'aborted' }))
    expect(drawn?.kind === 'agent' && 'result' in drawn ? drawn.result : null).toMatchObject({ agents: [{ outcome: 'stopped', body: 'Stored.' }] })
  })

  it('draws a configured agent as a subagent, with the task it states as its prompt', () => {
    const drawn = call('subagent_reviewer_1a2b', { prompt: 'Review the change.\nThen report.' }, { text: 'Looks right.', finishReason: 'completed' })
    expect(drawn?.kind).toBe('agent')
    expect(drawn?.request).toMatchObject({ description: 'Review the change.', prompt: 'Review the change.\nThen report.', registryKey: 'call_fixture_1' })
    expect(drawn?.kind === 'agent' && 'result' in drawn ? drawn.result : null).toMatchObject({ agents: [{ outcome: 'completed', body: 'Looks right.' }] })
  })

  it('draws a question and its answer', () => {
    const drawn = call('ask_question', { question: 'Which?', options: ['A', 'B'] }, 'B')
    expect(drawn?.request).toEqual({ questions: [{ question: 'Which?', options: [{ label: 'A' }, { label: 'B' }] }] })
    expect(drawn?.kind === 'question' && 'result' in drawn ? drawn.result : null).toEqual({ answers: [{ header: 'Which?', answer: 'B' }] })
  })

  it('words a refused plan tool as the plan\'s rejection', () => {
    const drawn = call('switch_to_act_mode', {}, { error: 'x' }, `Split it. -- ${CLINE_REJECTION_SUFFIX}`)
    expect(drawn?.status).toBe('declined')
    expect(drawn?.request).toMatchObject({ mode: 'Act', declinedTitle: 'Plan rejected' })
  })

  it('draws the change an edit asked for once Cline confirms it', () => {
    const drawn = call('editor', { path: '/w/a.ts', old_text: 'a', new_text: 'b' }, { query: 'edit:/w/a.ts', result: 'Edited /w/a.ts', success: true })
    expect(drawn?.kind === 'edit' && 'result' in drawn ? drawn.result : null).toMatchObject({ changes: [{ filePath: '/w/a.ts', oldStr: 'a', newStr: 'b' }] })
  })

  it('draws an insertion with no before side', () => {
    const drawn = call('editor', { path: '/w/a.ts', new_text: 'b', insert_line: 3 }, { query: 'insert:/w/a.ts', result: 'Inserted', success: true })
    expect(drawn?.request).toMatchObject({ changes: [{ filePath: '/w/a.ts', oldStr: '', newStr: 'b' }] })
  })

  it('draws the pages a fetch read', () => {
    const drawn = call('fetch_web_content', { requests: [{ url: 'https://a.test', prompt: 'x' }, { url: 'https://b.test', prompt: 'y' }] }, [{ query: 'https://a.test', result: 'A page', success: true }])
    expect(drawn?.request).toEqual({ url: 'https://a.test, https://b.test' })
  })

  it('draws a team message and a team run', () => {
    expect(call('team_send_message', { agentId: 'researcher', subject: 'Status', body: 'How far?' }, 'Sent.')?.request)
      .toEqual({ to: 'researcher', text: 'How far?', summary: 'Status' })
    expect(call('team_cancel_run', { runId: 'run_1' }, 'Cancelled.')?.request).toEqual({ action: 'stop', taskId: 'run_1' })
    expect(call('team_await_runs', {}, 'Done.')?.request).toEqual({ action: 'output' })
    expect(call('team_list_runs', {}, 'None.')?.request).toEqual({ action: 'list' })
  })

  it('draws a tool of a Model Context Protocol server as the generic card', () => {
    const drawn = call('github__search_issues', { q: 'bug' }, 'Found 2 issues.')
    expect(drawn?.kind).toBe('mcp')
  })

  // The card shows the tool's own words, and a failure's error without the words that
  // address the model.
  it('draws the words of a generic card, and a failure as its error', () => {
    const found = call('github__search_issues', { q: 'bug' }, { issues: 2 })
    expect([found?.kind, found?.status]).toEqual(['mcp', 'completed'])
    expect(found?.result).toEqual({ content: [{ type: 'text', text: '{\n  "issues": 2\n}' }] })
    const failed = call('github__search_issues', { q: 'bug' }, { error: 'x' }, 'Rate limited.')
    expect(failed?.status).toBe('failed')
    expect(failed?.kind === 'mcp' && 'result' in failed ? failed.result : null).toEqual({ content: [{ type: 'text', text: 'Rate limited.' }], error: 'Rate limited.' })
  })

  it('draws a read result that holds no record as its words', () => {
    const drawn = call('read_files', { files: [{ path: '/w/a.ts' }] }, 'The file is too large.')
    expect([drawn?.kind, drawn?.status, drawn?.degradation]).toEqual(['read', 'completed', undefined])
    expect(drawn?.result).toEqual({ unparsed: true, text: 'The file is too large.' })
  })

  // `editor` and `apply_patch` catch their own failure and answer with ONE record that
  // states `success: false`, and Cline 3.0.64 states an error for a call only when the
  // tool throws. That record is the whole call, so the edit did not land.
  it.each([
    ['editor', { path: '/w/a.ts', old_text: 'a', new_text: 'b' }, { query: 'edit:/w/a.ts', result: '', error: 'Editor operation failed: no match', success: false }],
    ['apply_patch', { input: PATCH }, { query: 'apply_patch', result: '', error: 'apply_patch failed: bad hunk', success: false }],
  ])('draws an %s call whose one record states a failure as failed, with its error', (name, args, record) => {
    const drawn = call(name, args, record)
    expect([drawn?.kind, drawn?.status]).toEqual(['edit', 'failed'])
    expect(drawn?.result).toEqual({ failure: true, text: record.error })
    // A stored transcript states the same record as its JSON text.
    expect(call(name, args, JSON.stringify(record))?.status).toBe('failed')
  })

  it('draws the words of every page a fetch read, the failed ones included', () => {
    const drawn = call('fetch_web_content', { requests: [{ url: 'https://a.test' }, { url: 'https://b.test' }] }, [
      { query: 'https://a.test', result: 'A page', success: true },
      { query: 'https://b.test', result: '', error: 'HTTP 404', success: false },
    ])
    expect(drawn?.kind === 'fetch' && 'result' in drawn ? drawn.result : null).toEqual({ result: 'A page\n\nHTTP 404' })
  })

  it('draws a team run as completed with its words', () => {
    const drawn = call('team_run_task', { agentId: 'researcher', task: 'Find the bug.' }, 'Queued run run_1.')
    expect(drawn?.kind === 'task' && 'result' in drawn ? drawn.result : null).toEqual({ outcome: 'completed', output: 'Queued run run_1.' })
  })
})
