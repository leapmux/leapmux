import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { acpTextContent, renderACPToolPair } from '../acp/testUtils'
import { input } from '../testUtils'

import '../testMocks'
import './plugin'

/** One finished call, read through the plugin with its opening frame paired. */
function finishedCall(opening: Record<string, unknown>, frame: Record<string, unknown>) {
  const request = { sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending', ...opening }
  return providerToolCall(AgentProvider.KIRO, { sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed', ...opening, ...frame }, {
    spanType: 'tool_call_update',
    request: input(request, null, AgentProvider.KIRO),
  })
}

describe('kiro tool rendering', () => {
  it('states the output and the exit code of a shell command from Kiro\'s own record', () => {
    const call = finishedCall({ title: 'Fail', kind: 'execute', rawInput: { command: 'false', description: 'Fail' } }, {
      content: acpTextContent('Output:\n\n\nExit Code: 1'),
      rawOutput: { output: '', exitCode: 1, message: 'Output:\n\n\nExit Code: 1' },
    })
    if (call?.kind !== 'execute' || !call.result || !('commands' in call.result))
      throw new Error('a shell call is an execute row')
    expect(call.result.commands[0]).toMatchObject({ exitCode: 1, output: '' })
    expect(call.request.command).toBe('false')
  })

  it('reads the lines of a file without the wrapper Kiro prints', () => {
    const call = finishedCall({ title: 'Read File', kind: 'read', rawInput: { path: '/w/a.txt', offset: 4, limit: null } }, {
      content: acpTextContent('<file name="/w/a.txt" language="plaintext" >\n<content>\nfive\nsix\n\n</content>\n</file>'),
    })
    if (call?.kind !== 'read' || !call.result || !('lines' in call.result))
      throw new Error('a read call is a read row')
    expect(call.request.path).toBe('/w/a.txt')
    expect(call.result.lines).toEqual([{ num: 5, text: 'five' }, { num: 6, text: 'six' }])
  })

  it('keeps the words of a read that is not a file body', () => {
    const call = finishedCall({ title: 'Read File', kind: 'read', rawInput: { path: '/w/none' } }, {
      content: acpTextContent('The file /w/none is currently empty or otherwise does not exist on disk and has no content.'),
    })
    expect(call?.kind === 'read' && call.result).toEqual({ unparsed: true, text: 'The file /w/none is currently empty or otherwise does not exist on disk and has no content.' })
  })

  it('draws the diff of a replacement with Kiro\'s own argument keys', () => {
    const call = finishedCall({ title: 'Replace in File', kind: 'edit', rawInput: { path: '/w/a.txt', oldStr: 'b', newStr: 'B' }, locations: [{ path: '/w/a.txt' }] }, {
      content: [{ type: 'diff', path: 'file:///w/a.txt', oldText: 'a\nb\n', newText: 'a\nB\n' }],
    })
    if (call?.kind !== 'edit')
      throw new Error('a replacement is an edit row')
    expect(call.request.changes[0]).toMatchObject({ filePath: '/w/a.txt', oldStr: 'b', newStr: 'B' })
  })

  it('draws a written file as its new content', () => {
    const call = finishedCall({ title: 'Write File', kind: 'edit', rawInput: { path: '/w/n.txt', text: 'new\n' } }, {
      content: [{ type: 'diff', path: 'file:///w/n.txt', oldText: '', newText: 'new\n' }],
    })
    expect(call?.kind).toBe('write')
    expect(call?.kind === 'write' && call.request.changes[0]).toMatchObject({ filePath: '/w/n.txt', oldStr: '', newStr: 'new\n' })
  })

  it('reads the target of a deletion as the file', () => {
    const call = finishedCall({ title: 'Delete File', kind: 'delete', rawInput: { targetFile: '/w/old.txt', explanation: 'unused' } }, { content: acpTextContent('Deleted') })
    expect(call?.kind === 'delete' && call.request.changes[0]?.filePath).toBe('/w/old.txt')
  })

  it('draws the entries of a directory listing', () => {
    const { container } = renderACPToolPair(AgentProvider.KIRO, { title: 'List Directory', kind: 'search', rawInput: { path: '/w' } }, {
      title: 'List Directory',
      kind: 'search',
      rawInput: { path: '/w' },
      content: acpTextContent('Contents of /w:\n  [FILE] a.ts\n  [DIR] src'),
    })
    expect(container.textContent).toContain('2 entries')
    expect(container.textContent).toContain('src/')
  })

  it('draws the matches of a grep search', () => {
    const call = finishedCall({ title: 'Grep Search', kind: 'search', rawInput: { query: 'needle', includePattern: '**/*.ts' } }, {
      content: acpTextContent('You searched for needle and received the following results:\n/w/a.ts\n3:needle'),
    })
    if (call?.kind !== 'grep' || !call.result || !('filenames' in call.result))
      throw new Error('a grep call is a grep row')
    expect(call.request).toEqual({ pattern: 'needle', paths: ['**/*.ts'] })
    expect(call.result.filenames).toEqual(['/w/a.ts'])
    expect(call.result.matchCount).toBe(1)
  })

  it('draws a finished subagent with its answer', () => {
    const opening = { title: 'Sub-agent: context-gatherer', kind: 'other', rawInput: { name: 'context-gatherer', prompt: 'find hello', explanation: 'delegate' }, _meta: { kiro: { kind: 'agent-subtask', agentSubtaskId: 'sub-1' } } }
    const call = finishedCall(opening, { rawOutput: 'CHILD RESULT: **found**' })
    if (call?.kind !== 'agent' || !call.result || !('agents' in call.result))
      throw new Error('a spawn is an agent row')
    expect(call.request).toMatchObject({ description: 'context-gatherer', prompt: 'find hello', registryKey: 'call', metadata: [{ label: 'Reason', value: 'delegate' }] })
    expect(call.result.agents[0]).toMatchObject({ agentId: 'sub-1', outcome: 'completed', body: 'CHILD RESULT: **found**' })
  })

  it('draws a failed subagent with its reason', () => {
    const opening = { title: 'Sub-agent: helper', kind: 'other', rawInput: { name: 'helper', prompt: 'go' }, _meta: { kiro: { kind: 'agent-subtask', agentSubtaskId: 's' } } }
    const call = finishedCall(opening, { status: 'failed', content: acpTextContent('Sub-agent execution was cancelled') })
    expect(call?.kind === 'agent' && call.result && 'agents' in call.result && call.result.agents[0]).toMatchObject({ outcome: 'failed', body: 'Sub-agent execution was cancelled' })
  })

  it('draws a question with its options, and no answer of its own', () => {
    const call = finishedCall({ title: 'Which DB?', kind: 'other', _meta: { kiro: { toolId: 'user_input', userInputOptions: [{ title: 'Postgres', recommended: true }, { title: 'SQLite' }] } } }, {})
    if (call?.kind !== 'question')
      throw new Error('a question is a question row')
    expect(call.request.questions).toEqual([{ question: 'Which DB?', options: [{ value: 'Postgres', label: 'Postgres (recommended)' }, { value: 'SQLite', label: 'SQLite' }], multiSelect: false }])
    expect(call.result).toBeUndefined()
  })

  it('draws an MCP tool by its server and tool, without Kiro\'s parse record', () => {
    const call = finishedCall({ title: '@probe/echo', kind: 'other', rawInput: { text: 'hi', _meta: { _isValid: true } }, _meta: { kiro: { serverName: 'probe' } } }, {
      content: acpTextContent('ECHO:hi'),
      rawOutput: { response: 'ECHO:hi', imageBase64Urls: ['data:image/png;base64,iVBORw0KGgo='], message: 'ECHO:hi' },
    })
    if (call?.kind !== 'mcp' || !call.result || !('content' in call.result))
      throw new Error('an MCP call is an MCP row')
    expect(call.request).toMatchObject({ server: 'probe', tool: 'echo', args: { text: 'hi' } })
    expect(call.result.content).toEqual([{ type: 'text', text: 'ECHO:hi' }, { type: 'image', source: { url: 'data:image/png;base64,iVBORw0KGgo=' } }])
  })

  it('draws an MCP tool that Kiro could not route by the name the model called', () => {
    const call = finishedCall({ title: 'probe___echo', kind: 'other', _meta: { kiro: {} } }, { status: 'failed', content: acpTextContent('Tool "probe___echo" is not available.') })
    expect(call?.kind === 'mcp' && call.request).toMatchObject({ server: 'probe', tool: 'echo' })
  })

  it('draws the whole list Kiro holds after a Task List call', () => {
    const call = finishedCall({ title: 'Task List', kind: 'other', rawInput: { command: 'complete', completed_task_ids: { 0: '1' } } }, {
      rawOutput: { tasks: [{ id: '1', task_description: 'one', completed: true }, { id: '2', task_description: 'two', completed: false }] },
    })
    if (call?.kind !== 'todo' || !call.result || !('items' in call.result))
      throw new Error('a to-do call is a to-do row')
    expect(call.request.items).toEqual([])
    expect(call.result.items.map(item => `${item.content}:${item.status}`)).toEqual(['one:completed', 'two:pending'])
  })

  it('draws the plan that a switch to execution carries', () => {
    const call = finishedCall({ title: 'Switch to Execution', kind: 'other', rawInput: { plan: '1. Do A\n2. Do B' } }, { content: acpTextContent('Switching to execution mode with the approved plan.') })
    expect(call?.kind).toBe('switch_mode')
    expect(call?.title).toBe('Switch to execution')
    expect(call?.kind === 'switch_mode' && call.result).toEqual({ text: '1. Do A\n2. Do B', format: 'markdown' })
  })

  it('draws a background process start as a command', () => {
    const call = finishedCall({ title: 'Control Process', kind: 'execute', rawInput: { action: 'start', command: 'npm run dev' } }, { content: acpTextContent('Started process term-1.') })
    expect(call?.kind === 'execute' && call.request.command).toBe('npm run dev')
    expect(call?.metadata).toContainEqual({ label: 'Background', value: 'Yes' })
  })

  it('draws a report of the session state in words', () => {
    const call = finishedCall({ title: 'Update Session Information', kind: 'other', rawInput: { title: 'T', description: 'D', status: 'completed' } }, { content: acpTextContent('Session information updated.') })
    expect(call?.kind === 'report' && call.request).toEqual({ payload: { title: 'T', description: 'D', status: 'completed' } })
    expect(call?.kind === 'report' && call.result).toEqual({ text: 'Session information updated.', format: 'markdown' })
  })
})
