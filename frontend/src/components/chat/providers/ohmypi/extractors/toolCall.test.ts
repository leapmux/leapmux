import type { OhMyPiToolRow } from './toolCall'
import { describe, expect, it } from 'vitest'
import { OH_MY_PI_TOOL } from '~/generated/contracts/ohmypi-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { isToolFailureResult, isUnparsedToolResult } from '../../../model/toolCall'
import { TOOL_KINDS } from '../../../model/toolKind'
import { DEFAULT_TOOL_REQUESTS } from '../../defaultToolRequests'
import { input } from '../../testUtils'
import { OH_MY_PI_TOOL_READERS, OH_MY_PI_TOOL_REQUEST_OVERRIDES, ohMyPiReclassify, ohMyPiToolCall, ohMyPiToolFacts, ohMyPiToolRow, ohMyPiToolSpanRowRole } from './toolCall'
import '~/components/chat/providers'

const text = (value: string) => [{ type: 'text', text: value }]

function start(toolName: string, args: Record<string, unknown> = {}): Record<string, unknown> {
  return { type: 'tool_execution_start', toolCallId: 'call_1', toolName, args }
}

function end(toolName: string, result: Record<string, unknown>, isError = false): Record<string, unknown> {
  return { type: 'tool_execution_end', toolCallId: 'call_1', toolName, result, isError }
}

function resultRow(toolName: string, args: Record<string, unknown>, result: Record<string, unknown>, isError = false): OhMyPiToolRow {
  return ohMyPiToolRow(end(toolName, result, isError), input(start(toolName, args), undefined, AgentProvider.OH_MY_PI), undefined)!
}

function requestRow(toolName: string, args: Record<string, unknown> = {}): OhMyPiToolRow {
  return ohMyPiToolRow(start(toolName, args), undefined, undefined)!
}

describe('ohMyPiToolRow', () => {
  it('refuses a frame that is not a tool frame', () => {
    expect(ohMyPiToolRow({ type: 'message_end' }, undefined, undefined)).toBeNull()
    expect(ohMyPiToolRow('tool_execution_start', undefined, undefined)).toBeNull()
  })

  it('reads an end frame, and a retained start frame, as the last row of its call', () => {
    expect(ohMyPiToolSpanRowRole(resultRow(OH_MY_PI_TOOL.Bash, {}, { content: text('ok') }))).toBe('result')
    expect(ohMyPiToolSpanRowRole(requestRow(OH_MY_PI_TOOL.Bash))).toBe('request')
    const retained = ohMyPiToolRow(start(OH_MY_PI_TOOL.Bash, { command: 'sleep 9' }), undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(ohMyPiToolSpanRowRole(retained)).toBe('result')
  })
})

describe('ohMyPiToolFacts', () => {
  it('reads the arguments from the paired start frame', () => {
    const facts = ohMyPiToolFacts(resultRow(OH_MY_PI_TOOL.Read, { path: 'a.ts' }, { content: text('1:x') }), undefined)
    expect(facts.args).toEqual({ path: 'a.ts' })
    expect(facts.resultAvailable).toBe(true)
  })

  it('ignores a paired frame of another call', () => {
    const other = input({ type: 'tool_execution_start', toolCallId: 'call_2', toolName: OH_MY_PI_TOOL.Read, args: { path: 'b.ts' } }, undefined, AgentProvider.OH_MY_PI)
    const row = ohMyPiToolRow(end(OH_MY_PI_TOOL.Read, { content: text('x') }), other, undefined)!
    expect(ohMyPiToolFacts(row, undefined).args).toEqual({})
  })

  it('reads the result of a start row whose end already landed', () => {
    const endFrame = input(end(OH_MY_PI_TOOL.Bash, { content: text('done'), details: {} }), undefined, AgentProvider.OH_MY_PI)
    const row = ohMyPiToolRow(start(OH_MY_PI_TOOL.Bash, { command: 'ls' }), undefined, endFrame)!
    const facts = ohMyPiToolFacts(row, undefined)
    expect(facts.text).toBe('done')
    expect(facts.lifecycle.resultFrameLanded).toBe(true)
  })

  it('reads a partial result as the text a stopped call left', () => {
    const row = ohMyPiToolRow({ ...start(OH_MY_PI_TOOL.Bash, { command: 'sleep 9' }), result: { content: text('so far'), details: {} } }, undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(ohMyPiToolFacts(row, MessageCompletion.INTERRUPTED)).toMatchObject({ text: 'so far', resultAvailable: true, finished: true })
  })

  it('reads the failure of a start row whose end frame landed failed', () => {
    const endFrame = input(end(OH_MY_PI_TOOL.Bash, { content: text('boom') }, true), undefined, AgentProvider.OH_MY_PI)
    const row = ohMyPiToolRow(start(OH_MY_PI_TOOL.Bash, { command: 'false' }), undefined, endFrame)!
    expect(ohMyPiToolFacts(row, undefined)).toMatchObject({ isError: true, text: 'boom', resultAvailable: true, finished: false })
    expect(ohMyPiToolCall(row).status).toBe('failed')
  })

  it('ignores a paired end frame of another call', () => {
    const sibling = input({ ...end(OH_MY_PI_TOOL.Bash, { content: text('other') }, true), toolCallId: 'call_2' }, undefined, AgentProvider.OH_MY_PI)
    const row = ohMyPiToolRow(start(OH_MY_PI_TOOL.Bash, { command: 'ls' }), undefined, sibling)!
    expect(ohMyPiToolFacts(row, undefined)).toMatchObject({ isError: false, text: '', resultAvailable: false })
    expect(ohMyPiToolFacts(row, undefined).lifecycle.resultFrameLanded).toBe(false)
  })

  it('reads the pictures a result carries, with the path of the call', () => {
    const row = resultRow(OH_MY_PI_TOOL.Read, { path: 'logo.png' }, { content: [{ type: 'image', data: 'iVBORw0KGgo=', mimeType: 'image/png' }, ...text('image')] })
    const images = ohMyPiToolFacts(row, undefined).images
    expect(images).toHaveLength(1)
    expect(images[0]).toMatchObject({ mimeType: 'image/png' })
  })
})

describe('ohMyPiReclassify', () => {
  const facts = (toolName: string, args: Record<string, unknown>, details: Record<string, unknown> = {}) =>
    ohMyPiToolFacts(resultRow(toolName, args, { content: text('x'), details }), undefined)

  it('gives a tool the table does not hold the generic card', () => {
    expect(ohMyPiReclassify(facts('mcp__github_search', {}))).toBe('mcp')
    expect(ohMyPiReclassify(facts('my_extension_tool', {}))).toBe('mcp')
  })

  it('reads a write to a device path as the device call it is', () => {
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Write, { path: 'xd://lsp', content: '{}' }))).toBe('mcp')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Write, { path: 'a.ts', content: 'x' }))).toBe('write')
  })

  it('reads a read of a URL as a fetch', () => {
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'https://example.com' }))).toBe('fetch')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'example' }, { kind: 'url', url: 'https://example.com' }))).toBe('fetch')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'a.ts' }))).toBe('read')
  })

  it('reads a URL in any case, and keeps a path that only holds a scheme of another kind', () => {
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'HTTP://EXAMPLE.COM' }))).toBe('fetch')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'ftp://example.com/a' }))).toBe('read')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'docs/https://x' }))).toBe('read')
  })

  it('keeps a device path of a read as a read, because only a write runs a device tool', () => {
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Read, { path: 'xd://lsp' }))).toBe('read')
  })

  it('keeps every declared kind', () => {
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Bash, {}))).toBe('execute')
    expect(ohMyPiReclassify(facts(OH_MY_PI_TOOL.Task, {}))).toBe('agent')
  })
})

describe('OH_MY_PI_TOOL_READERS', () => {
  it('holds one entry for every kind, and each answers its own kind', () => {
    expect(Object.keys(OH_MY_PI_TOOL_READERS).sort()).toEqual([...TOOL_KINDS].sort())
    const facts = ohMyPiToolFacts(requestRow(OH_MY_PI_TOOL.Think, {}), undefined)
    for (const kind of TOOL_KINDS)
      expect(OH_MY_PI_TOOL_READERS[kind](facts).kind).toBe(kind)
  })

  it('fills the shared request for every kind omp does not override', () => {
    const facts = ohMyPiToolFacts(requestRow(OH_MY_PI_TOOL.Think, { thought: 'x', path: 'a' }), undefined)
    for (const kind of TOOL_KINDS) {
      if (Object.hasOwn(OH_MY_PI_TOOL_REQUEST_OVERRIDES, kind) || kind === 'fetch')
        continue
      expect(OH_MY_PI_TOOL_READERS[kind](facts).request, kind).toEqual(DEFAULT_TOOL_REQUESTS[kind](facts.args))
    }
  })
})

describe('ohMyPiToolCall', () => {
  it('states no result while a call runs', () => {
    const call = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Bash, { command: 'ls' }))
    expect(call.kind).toBe('execute')
    expect(call.request).toMatchObject({ command: 'ls' })
    expect('result' in call ? call.result : undefined).toBeUndefined()
  })

  it('reads a failed command with its exit code', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Bash, { command: 'exit 3' }, { content: text('out\n\n\nWall time: 0.07 seconds\n\nCommand exited with code 3'), details: { exitCode: 3, wallTimeMs: 65 } }, true))
    expect(call.status).toBe('failed')
    expect(call.kind === 'execute' && 'result' in call ? call.result : null).toEqual({ commands: [{ output: 'out', exitCode: 3, durationMs: 65 }], unresolvedTerminals: [] })
  })

  it('reads a command the reader stopped as cancelled', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Bash, { command: 'sleep 9' }, { content: text('part\n\n[Command aborted]') }, true))
    expect(call.status).toBe('cancelled')
  })

  it('states no exit code for a command omp moved to the background', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Bash, { command: 'npm run build' }, { content: text('Moved to the background.'), details: { async: { state: 'running', jobId: 'bash-1', type: 'bash' } } }))
    expect(call.kind === 'execute' && 'result' in call ? call.result : null).toEqual({ commands: [{ output: 'Moved to the background.' }], unresolvedTerminals: [] })
  })

  it('reads the cells of an eval call', () => {
    // omp 18.2.11's `EvalCellResult`: the first cell is index 0.
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Eval, { language: 'py', code: 'print(2)' }, { content: text('2'), details: { cells: [{ index: 0, code: 'print(2)', output: '2', status: 'complete', exitCode: 0 }] } }))
    expect(call.request).toEqual({ command: 'print(2)' })
    expect(call.kind === 'execute' && 'result' in call ? call.result : null).toEqual({ commands: [{ output: '2', label: 'Cell 1', exitCode: 0 }], unresolvedTerminals: [] })
  })

  it('reads the files of a hashline edit into its request, and the snapshots into its result', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Edit, { input: '[notes.txt#C789]\nPUT 2.=2:\n+beta TWO' }, {
      content: text('[notes.txt#9C79]\n1:alpha one'),
      details: { path: '/p/notes.txt', oldText: 'a\nb\n', newText: 'a\nB\n', op: 'update' },
    }))
    expect(call.kind).toBe('edit')
    expect(call.request).toEqual({ changes: [{ filePath: 'notes.txt', operation: 'edit', oldStr: '', newStr: '', structuredPatch: null }] })
    expect('result' in call ? call.result : null).toEqual({ changes: [{ filePath: '/p/notes.txt', operation: 'edit', oldStr: 'a\nb\n', newStr: 'a\nB\n', structuredPatch: null }] })
  })

  it('reads a replace-mode edit through the shared request', () => {
    const call = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Edit, { path: 'a.ts', old_string: 'x', new_string: 'y' }))
    expect(call.request).toEqual({ changes: [{ filePath: 'a.ts', operation: 'edit', oldStr: 'x', newStr: 'y', structuredPatch: null }] })
  })

  it('keeps the words of an edit whose snapshots omp dropped', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Edit, { input: '[a.ts#1A2B]\nPUT 1.=1:\n+x' }, { content: text('[a.ts#0000]\n1:x'), details: { path: '/p/a.ts', snapshotsPruned: true } }))
    expect('result' in call && isUnparsedToolResult(call.result)).toBe(true)
  })

  it('reads a write as the content it added', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Write, { path: 'new.txt', content: 'fresh\n' }, { content: text('ok'), details: { resolvedPath: '/p/new.txt' } }))
    expect(call.request).toEqual({ changes: [{ filePath: 'new.txt', operation: 'add', oldStr: '', newStr: 'fresh\n', structuredPatch: null }] })
    expect('result' in call ? call.result : null).toEqual({ changes: [{ filePath: '/p/new.txt', operation: 'add', oldStr: '', newStr: 'fresh\n', structuredPatch: null }] })
  })

  it('reads a failed read as its reason', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Read, { path: 'nope' }, { content: text('File not found') }, true))
    expect(call.status).toBe('failed')
    expect('result' in call && isToolFailureResult(call.result) ? call.result.text : null).toBe('File not found')
  })

  it('reads a fetched page', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Read, { path: 'https://example.com' }, { content: text('# Example'), details: { kind: 'url', url: 'https://example.com' } }))
    expect(call.kind).toBe('fetch')
    expect(call.request).toEqual({ url: 'https://example.com' })
    expect('result' in call ? call.result : null).toEqual({ result: '# Example' })
  })

  it('states no result for a fetch that runs, and the reason of one that failed', () => {
    const running = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Read, { path: 'https://example.com' }))
    expect(running.kind).toBe('fetch')
    expect(running.request).toEqual({ url: 'https://example.com' })
    expect('result' in running ? running.result : undefined).toBeUndefined()
    const failed = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Read, { path: 'https://example.com' }, { content: text('404 Not Found') }, true))
    expect(failed.kind).toBe('fetch')
    expect(failed.status).toBe('failed')
    expect('result' in failed && isToolFailureResult(failed.result) ? failed.result.text : null).toBe('404 Not Found')
  })

  it('reads the URL of a fetch from its result when the row states no arguments', () => {
    // An end row whose start frame the store did not resolve.
    const row = ohMyPiToolRow(end(OH_MY_PI_TOOL.Read, { content: text('# Example'), details: { kind: 'url', url: 'https://example.com' } }), undefined, undefined)!
    const call = ohMyPiToolCall(row)
    expect(call.kind).toBe('fetch')
    expect(call.request).toEqual({ url: 'https://example.com' })
  })

  it('reads a to-do call from the list its result states', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Todo, { op: 'start', task: 'Write code' }, {
      content: text('Remaining items (1)'),
      details: { op: 'start', phases: [{ name: 'Build', tasks: [{ content: 'Write code', status: 'in_progress' }] }] },
    }))
    expect(call.kind).toBe('todo')
    expect(call.request).toMatchObject({ items: [{ content: 'Write code', status: 'in_progress' }], note: 'start' })
  })

  it('keeps the words of a to-do result that states no list', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Todo, { op: 'view' }, { content: text('Nothing to show'), details: {} }))
    expect('result' in call && isUnparsedToolResult(call.result)).toBe(true)
  })

  it('reads a failed to-do call as its reason', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Todo, { op: 'done', task: 'Nope' }, { content: text('Unknown task: Nope') }, true))
    expect(call.status).toBe('failed')
    expect('result' in call && isToolFailureResult(call.result)).toBe(true)
  })

  it('reads a web search whose details state no response as its text', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.WebSearch, { query: 'leapmux' }, { content: text('Three results.'), details: {} }))
    expect('result' in call ? call.result : null).toEqual({ links: [], summary: 'Three results.' })
  })

  it('reads a web search\'s links and answer', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.WebSearch, { query: 'leapmux' }, {
      content: text('fallback'),
      details: { response: { answer: 'The answer.', sources: [{ title: 'Doc', url: 'https://example.com/doc' }, { title: 'No URL' }] } },
    }))
    expect('result' in call ? call.result : null).toEqual({ links: [{ title: 'Doc', url: 'https://example.com/doc' }], summary: 'The answer.' })
  })

  it('reads the question tool, headed by its one question', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Ask, { questions: [{ id: 'db', question: 'Which database?', options: [{ label: 'SQLite' }] }] }, {
      content: text('User selected: SQLite'),
      details: { question: 'Which database?', selectedOptions: ['SQLite'] },
    }))
    expect(call.title).toBe('Which database?')
    expect('result' in call ? call.result : null).toEqual({ answers: [{ header: 'Which database?', answer: 'SQLite' }] })
  })

  it('keeps the tool\'s name above a call of several questions', () => {
    const questions = [{ id: 'a', question: 'First?', options: [] }, { id: 'b', question: 'Second?', options: [] }]
    expect(ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Ask, { questions })).title).toBe('Ask')
  })

  it('reads the result text as the answer when the details state none', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Ask, { questions: [{ id: 'db', question: 'Which database?', header: 'Storage', options: [] }] }, { content: text('User selected: SQLite'), details: {} }))
    expect('result' in call ? call.result : null).toEqual({ answers: [{ header: 'Storage', answer: 'User selected: SQLite' }] })
    // A call that states no question and no text still answers under a heading.
    const bare = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Ask, {}, { content: [], details: {} }))
    expect('result' in bare ? bare.result : null).toEqual({ answers: [{ header: 'Answer', answer: null }] })
  })

  it('reads a subagent launch', () => {
    const call = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Task, { tasks: [{ name: 'ScoutOne', task: 'Say hello.' }] }))
    expect(call.kind).toBe('agent')
    expect(call.title).toBe('ScoutOne')
  })

  it('reads a subagent launch that failed as its reason', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Task, { tasks: [{ name: 'ScoutOne', task: 'Say hello.' }] }, { content: text('No such agent: scout'), details: {} }, true))
    expect(call.status).toBe('failed')
    expect(call.title).toBe('ScoutOne')
    expect('result' in call && isToolFailureResult(call.result) ? call.result.text : null).toBe('No such agent: scout')
  })

  it('keeps the words of a subagent result that states no run, and states no result with no words', () => {
    const unread = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Task, { tasks: [{ name: 'A', task: 'x' }] }, { content: text('Something new.'), details: {} }))
    expect('result' in unread && isUnparsedToolResult(unread.result) ? unread.result.text : null).toBe('Something new.')
    const empty = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Task, { tasks: [{ name: 'A', task: 'x' }] }, { content: [], details: {} }))
    expect('result' in empty ? empty.result : undefined).toBeUndefined()
  })

  it('reads the result text of an eval call that states no cells', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Eval, { language: 'js', code: '1 + 1' }, { content: text('2'), details: {} }))
    expect(call.request).toEqual({ command: '1 + 1', language: 'javascript' })
    expect(call.kind === 'execute' && 'result' in call ? call.result : null).toEqual({ commands: [{ output: '2', exitCode: 0 }], unresolvedTerminals: [] })
  })

  it('states no result for an edit whose result states no snapshot and no words', () => {
    const edit = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Edit, { path: 'a.ts', old_string: 'x', new_string: 'y' }, { content: [], details: {} }))
    expect(edit.kind).toBe('edit')
    expect('result' in edit ? edit.result : undefined).toBeUndefined()
  })

  it('reads a hub message', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Hub, { op: 'send', to: 'ScoutOne', message: 'Hurry.' }, { content: text('Sent.') }))
    expect(call.request).toEqual({ to: 'ScoutOne', text: 'Hurry.', summary: 'send' })
    expect('result' in call ? call.result : null).toEqual({ text: 'Sent.', format: 'plain' })
  })

  it('reads a yield that omp accepted as a completed report', () => {
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Yield, { data: 'child says hello' }, { content: text('Result submitted.'), details: { data: 'child says hello', status: 'success' } }))
    expect(call.status).toBe('completed')
    expect('result' in call ? call.result : null).toEqual({ text: 'Result submitted.', format: 'markdown' })
  })

  it('reads a yield that reports an error as a failed report, with the error', () => {
    // omp 18.2.11 (`tools/yield.ts`) answers `yield {error}` with a result that is
    // NOT flagged as an error: it states `status: "aborted"` and the error instead.
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Yield, { error: 'cannot reach the API' }, {
      content: text('Task aborted: cannot reach the API'),
      details: { status: 'aborted', error: 'cannot reach the API' },
    }))
    expect(call.status).toBe('failed')
    expect('result' in call && isToolFailureResult(call.result) ? call.result.text : null).toBe('cannot reach the API')
    const noError = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Yield, {}, { content: text('Task aborted: x'), details: { status: 'aborted' } }))
    expect('result' in noError && isToolFailureResult(noError.result) ? noError.result.text : null).toBe('Task aborted: x')
  })

  it('states the intent the model gave for a call, on its start row and on its end row', () => {
    const withIntent = { ...start(OH_MY_PI_TOOL.Bash, { command: 'ls' }), intent: 'list the files' }
    expect(ohMyPiToolCall(ohMyPiToolRow(withIntent, undefined, undefined)!).metadata).toEqual([{ label: 'Intent', value: 'list the files' }])
    const endRow = ohMyPiToolRow(end(OH_MY_PI_TOOL.Bash, { content: text('a.ts') }), input(withIntent, undefined, AgentProvider.OH_MY_PI), undefined)!
    expect(ohMyPiToolCall(endRow).metadata).toEqual([{ label: 'Intent', value: 'list the files' }])
    expect(ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Bash, { command: 'ls' })).metadata).toBeUndefined()
    expect(ohMyPiToolCall(ohMyPiToolRow({ ...withIntent, intent: '  ' }, undefined, undefined)!).metadata).toBeUndefined()
  })

  it('reads a Model Context Protocol tool with the name after its prefix', () => {
    const call = ohMyPiToolCall(resultRow('mcp__my_server_search', { q: 'x' }, { content: text('found') }))
    expect(call.kind).toBe('mcp')
    expect(call.request).toEqual({ server: '', tool: 'my_server_search', args: { q: 'x' } })
    expect('result' in call ? call.result : null).toEqual({ content: [{ type: 'text', text: 'found' }] })
  })

  it('states no generic result while the call runs', () => {
    const row = ohMyPiToolRow({ type: 'tool_execution_update', toolCallId: 'call_1', toolName: 'my_tool', args: {}, partialResult: { content: text('...') } }, undefined, undefined)!
    const call = ohMyPiToolCall(row)
    expect('result' in call ? call.result : undefined).toBeUndefined()
  })

  it('reads a failed generic call with its error', () => {
    const call = ohMyPiToolCall(resultRow('my_tool', {}, { content: text('broken') }, true))
    expect(call.status).toBe('failed')
    expect('result' in call ? call.result : null).toEqual({ content: [{ type: 'text', text: 'broken' }], error: 'broken' })
  })

  it('reads a call the turn stopped from the completion LeapMux recorded', () => {
    const row = ohMyPiToolRow(start(OH_MY_PI_TOOL.Read, { path: 'a.ts' }), undefined, undefined, MessageCompletion.INTERRUPTED)!
    expect(ohMyPiToolCall(row, MessageCompletion.INTERRUPTED).status).toBe('cancelled')
  })
})

describe('ohMyPiToolCall for the hub', () => {
  it('draws a process start as the command it runs', () => {
    // omp 18.2.11's own example call (`tools/hub/index.ts`).
    const call = ohMyPiToolCall(resultRow(OH_MY_PI_TOOL.Hub, { op: 'start', name: 'web', application: 'bun', args: ['run', 'dev'], ready: { log: 'Local:.*http', port: 5173, timeout: 30 } }, {
      content: text('Started web.'),
      details: { op: 'start' },
    }))
    expect(call.kind).toBe('execute')
    expect(call.request).toEqual({ command: 'bun run dev', description: 'Start the process web' })
    expect(call.kind === 'execute' && 'result' in call ? call.result : null).toEqual({ commands: [{ output: 'Started web.' }], unresolvedTerminals: [] })
  })

  it('quotes an argument that a shell would split, and states the directory', () => {
    const call = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op: 'start', name: 'probe', application: 'node', args: ['-e', 'console.log(\'hi there\')', ''], cwd: '/p/app' }))
    expect(call.request).toEqual({ command: String.raw`node -e 'console.log('\''hi there'\'')' ''`, description: 'Start the process probe', cwd: '/p/app' })
  })

  it('draws input to a process as the text it sends', () => {
    const typed = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op: 'send', name: 'debugger', text: 'breakpoint set --name main' }))
    expect(typed.kind).toBe('execute')
    expect(typed.request).toEqual({ command: 'breakpoint set --name main', description: 'Send to the process debugger' })
    const keys = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op: 'send', name: 'debugger', keys: ['CTRL_C'] }))
    expect(keys.request).toEqual({ command: 'CTRL_C', description: 'Send to the process debugger' })
    const signal = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op: 'send', name: 'web', signal: 'SIGTERM' }))
    expect(signal.request).toEqual({ command: 'SIGTERM', description: 'Send to the process web' })
  })

  it('draws every other process operation as the operation and the process', () => {
    for (const op of ['logs', 'stop', 'restart', 'describe', 'wait']) {
      const call = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op, name: 'web' }))
      expect(call.kind, op).toBe('execute')
      expect(call.request, op).toEqual({ command: `${op} web` })
    }
    const list = ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, { op: 'ps' }))
    expect(list.kind).toBe('execute')
    expect(list.request).toEqual({ command: 'ps' })
  })

  it('keeps agent messages and job control as a message', () => {
    const calls = [
      { op: 'send', to: 'ScoutOne', message: 'Hurry.' },
      { op: 'wait' },
      { op: 'wait', from: 'ScoutOne' },
      { op: 'inbox', peek: true },
      { op: 'list' },
      { op: 'jobs' },
      { op: 'cancel', ids: ['bash_a1b2c3'] },
      // omp refuses a send that states both a process and a recipient, as a message.
      { op: 'send', name: 'web', to: 'ScoutOne', message: 'x' },
    ]
    for (const args of calls)
      expect(ohMyPiToolCall(requestRow(OH_MY_PI_TOOL.Hub, args)).kind, JSON.stringify(args)).toBe('message')
  })
})
