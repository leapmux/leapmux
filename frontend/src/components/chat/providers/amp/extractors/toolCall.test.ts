import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../../testUtils'
import { ampToolResultRow, ampToolUseRow } from '../toolResults.fixtures'
import { AMP_TOOL_REQUEST_OVERRIDES, ampResultOutcome, ampToolCall, ampToolCallKind, ampToolFacts, ampToolRow, ampToolSpanRowRole } from './toolCall'

const provider = AgentProvider.AMP

describe('ampResultOutcome', () => {
  it('reads a refusal, a cancellation and a failure from the result', () => {
    const outcome = (content: string, isError = true) => ampResultOutcome({ toolUseId: 'TU-1', content, isError })
    expect(outcome('Tool rejected by plugin: Matches built-in permissions rule 75: ask shell_command', false)).toBe('declined')
    expect(outcome('Plugin error: Use the clean target.\n')).toBe('declined')
    expect(outcome('  Tool execution rejected by user: no')).toBe('declined')
    expect(outcome('Tool execution cancelled: User canceled')).toBe('interrupted')
    expect(outcome('Error: boom')).toBe('failed')
    expect(outcome('{"output":"","exitCode":1}', false)).toBeNull()
    expect(ampResultOutcome(undefined)).toBeNull()
  })
})

describe('ampToolRow', () => {
  it('pairs a request with the result of the same call only', () => {
    const request = ampToolUseRow('shell_command', { command: 'ls' }, 'TU-a')
    const mine = input(ampToolResultRow('ok', false, 'TU-a'), undefined, provider)
    const sibling = input(ampToolResultRow('other', false, 'TU-b'), undefined, provider)
    expect(ampToolRow(request, undefined, mine)?.result?.content).toBe('ok')
    expect(ampToolRow(request, undefined, sibling)?.result).toBeUndefined()
  })

  it('reads a retained request row as the call\'s end', () => {
    const request = ampToolUseRow('shell_command', { command: 'sleep 40' })
    expect(ampToolRow(request, undefined, undefined)?.finished).toBe(false)
    const retained = ampToolRow(request, undefined, undefined, MessageCompletion.INTERRUPTED)
    expect(retained?.finished).toBe(true)
    expect(ampToolSpanRowRole(retained!)).toBe('result')
  })

  it('keeps a result whose call the store did not resolve, as a call with no name', () => {
    const row = ampToolRow(ampToolResultRow('ok', false, 'TU-z'), undefined, undefined)
    expect(row?.call).toEqual({ id: 'TU-z', name: '', input: {} })
    expect(row?.finished).toBe(true)
  })

  it('answers null for a row that is no tool row', () => {
    expect(ampToolRow({ type: 'result' }, undefined, undefined)).toBeNull()
    expect(ampToolRow(undefined, undefined, undefined)).toBeNull()
  })
})

describe('ampToolCallKind', () => {
  it('reads a listed tool by its table, and every other tool as the generic card', () => {
    const facts = (name: string) => ampToolFacts({ call: { id: 'TU-1', name, input: {} }, result: undefined, finished: false }, undefined)
    expect(ampToolCallKind(facts('shell_command'))).toBe('execute')
    expect(ampToolCallKind(facts('Task'))).toBe('agent')
    expect(ampToolCallKind(facts('mcp__db__query'))).toBe('mcp')
    expect(ampToolCallKind(facts(''))).toBe('mcp')
  })
})

describe('AMP_TOOL_REQUEST_OVERRIDES', () => {
  // The whole deviation list: each entry reads an Amp argument the shared table
  // does not spell. A new entry needs a reason at its declaration.
  it('overrides only the kinds whose arguments Amp spells its own way', () => {
    expect(Object.keys(AMP_TOOL_REQUEST_OVERRIDES).sort()).toEqual(['agent', 'edit', 'execute', 'glob', 'mcp', 'read', 'task', 'web_search', 'write'])
  })
})

describe('amp tool requests', () => {
  const request = (name: string, args: Record<string, unknown>) =>
    ampToolCall({ call: { id: 'TU-1', name, input: args }, result: undefined, finished: false }).request

  it('states a replace-all edit, and no flag for an edit of one match', () => {
    expect(request('edit_file', { path: '/w/a.ts', old_str: 'a', new_str: 'b', replace_all: true })).toMatchObject({ replaceAll: true })
    expect(request('edit_file', { path: '/w/a.ts', old_str: 'a', new_str: 'b' })).not.toHaveProperty('replaceAll')
    // Only `true` states the flag: Amp's own schema holds a boolean.
    expect(request('edit_file', { path: '/w/a.ts', old_str: 'a', new_str: 'b', replace_all: 'true' })).not.toHaveProperty('replaceAll')
  })

  // The shared model refuses a file change that states no file, so such a call draws
  // the uncategorized card with the call's own words, and never an empty diff.
  it.each([
    ['edit_file', { old_str: 'a', new_str: 'b' }, 'edit'],
    ['apply_patch', { patchText: 'not a patch' }, 'edit'],
    ['create_file', { content: 'x' }, 'write'],
  ] as const)('degrades a %s call that states no file to the uncategorized row', (name, args, originalKind) => {
    const drawn = ampToolCall({ call: { id: 'TU-1', name, input: args }, result: { toolUseId: 'TU-1', content: 'Done.', isError: false }, finished: true })
    expect(drawn.kind).toBe('other')
    expect(drawn.degradation).toEqual({ fault: 'a-file-change-states-no-file', originalKind })
    expect(drawn.result).toEqual({ unparsed: true, text: 'Done.' })
  })

  it('reads the pattern of a glob under either spelling', () => {
    expect(request('glob', { filePattern: '**/*.ts', pattern: '*.go' })).toEqual({ pattern: '**/*.ts', paths: [] })
    expect(request('Glob', { pattern: '*.go' })).toEqual({ pattern: '*.go', paths: [] })
    expect(request('glob', {})).toEqual({ pattern: '', paths: [] })
  })

  it('reads the first query of a web search that states no objective', () => {
    expect(request('web_search', { search_queries: ['leapmux', 'docs'] })).toEqual({ query: 'leapmux', queries: ['leapmux', 'docs'] })
    expect(request('web_search', {})).toEqual({ query: '' })
  })

  it('states no directory, process or timeout that the call does not state', () => {
    expect(request('shell_command', { command: 'ls' })).toEqual({ command: 'ls' })
    expect(request('shell_command_status', {})).toEqual({ action: 'output' })
    expect(request('shell_command_kill', { pid: 0 })).toEqual({ action: 'stop', taskId: '0' })
  })
})

describe('ampToolCall', () => {
  const call = (name: string, args: Record<string, unknown>, content?: string, isError = false) =>
    ampToolCall({ call: { id: 'TU-1', name, input: args }, result: content === undefined ? undefined : { toolUseId: 'TU-1', content, isError }, finished: content !== undefined })

  it('reads a shell call with its directory and its exit code', () => {
    const shell = call('shell_command', { command: 'ls', workdir: '/work' }, '{"output":"a\\n","exitCode":0}')
    expect(shell.kind).toBe('execute')
    expect(shell.request).toEqual({ command: 'ls', cwd: '/work' })
    expect(shell.status).toBe('completed')
    expect(shell.kind === 'execute' ? shell.result : undefined).toEqual({ commands: [{ output: 'a\n', exitCode: 0 }], unresolvedTerminals: [] })
  })

  it('reads a status and a kill of a background command', () => {
    const status = call('shell_command_status', { pid: 42, timeout_ms: 1000 }, '{"output":"more","running":true,"pid":42}')
    expect(status.request).toEqual({ action: 'output', taskId: '42', timeoutMs: 1000 })
    const kill = call('shell_command_kill', { pid: 42 }, '{"output":"","exitCode":143,"running":false,"pid":42}')
    expect(kill.request).toEqual({ action: 'stop', taskId: '42' })
    expect(kill.kind === 'task' ? kill.result : undefined).toEqual({ outcome: 'stopped', output: '' })
  })

  it('reads the queries of a web search and the links it found', () => {
    const search = call('web_search', { objective: 'leapmux docs', search_queries: ['leapmux', 'docs'] }, '[{"title":"Doc","url":"https://example.com/doc"},{"title":"no url"}]')
    expect(search.request).toEqual({ query: 'leapmux docs', queries: ['leapmux', 'docs'] })
    expect(search.kind === 'web_search' ? search.result : undefined).toEqual({ links: [{ title: 'Doc', url: 'https://example.com/doc' }], summary: '' })
    const prose = call('web_search', { objective: 'x' }, 'No results.')
    expect(prose.kind === 'web_search' ? prose.result : undefined).toEqual({ links: [], summary: 'No results.' })
  })

  it('reads a declined call as declined with the reason, and draws no typed payload', () => {
    const declined = call('apply_patch', { patchText: '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-a\n+b\n*** End Patch' }, 'Plugin error: not this file', true)
    expect(declined.status).toBe('declined')
    expect(declined.result).toEqual({ failure: true, text: 'Plugin error: not this file' })
    expect(declined.degradation).toBeUndefined()
  })

  it('reads a running call with no result, and draws the request alone', () => {
    const running = call('Read', { path: '/w/a.ts', read_range: [2, 5] })
    expect(running.status).toBe('unstated')
    expect(running.request).toEqual({ path: '/w/a.ts', offset: 2, limit: 4 })
    expect(running.result).toBeUndefined()
  })

  it('reads a skill and a sleep as prose', () => {
    expect(call('skill', { name: 'release' }, 'Loaded.').result).toEqual({ text: 'Loaded.', format: 'markdown' })
    expect(call('sleep', {}, 'Slept.').result).toEqual({ text: 'Slept.', format: 'plain' })
  })

  it('reads a tool of a plugin as the generic card, with a failure as its error', () => {
    const generic = call('upload_thread_file', { path: '/a' }, 'Upload refused.', true)
    expect(generic.kind).toBe('mcp')
    expect(generic.request).toMatchObject({ server: '', tool: 'upload_thread_file', args: { path: '/a' } })
    expect(generic.status).toBe('failed')
  })

  it('reads the view of an image as a read that holds the picture', () => {
    const view = call('view_media', { path: '/w/shot.png' }, JSON.stringify({ absolutePath: '/w/shot.png', content: 'iVBORw0KGgo=', isImage: true, imageInfo: { mimeType: 'image/png' } }))
    expect(view.kind).toBe('read')
    expect(view.images).toHaveLength(1)
  })

  // A result that no reader of the kind can read keeps its words, as a result the row
  // could not parse, and an empty one states no result at all.
  it('draws a Read result that is not Amp\'s record as its words', () => {
    const read = call('Read', { path: '/w/a.ts' }, 'File is too large to read.')
    expect([read.kind, read.status, read.degradation]).toEqual(['read', 'completed', undefined])
    expect(read.result).toEqual({ unparsed: true, text: 'File is too large to read.' })
    expect(call('Read', { path: '/w/a.ts' }, '').result).toBeUndefined()
  })

  it('draws a patch result that lists no file as its words', () => {
    const empty = JSON.stringify({ summary: '', files: [] })
    const patch = call('apply_patch', { patchText: '*** Begin Patch\n*** Update File: /w/a.ts\n@@\n-a\n+b\n*** End Patch' }, empty)
    expect([patch.kind, patch.status, patch.degradation]).toEqual(['edit', 'completed', undefined])
    expect(patch.result).toEqual({ unparsed: true, text: empty })
  })

  it('reads the links of a web search that answers with a `results` record', () => {
    const search = call('web_search', { objective: 'x' }, JSON.stringify({ results: [{ url: 'https://example.com/a' }, 'junk'] }))
    expect(search.kind === 'web_search' ? search.result : undefined).toEqual({ links: [{ title: 'https://example.com/a', url: 'https://example.com/a' }], summary: '' })
  })

  it('titles a call whose tool the store did not resolve as a tool', () => {
    const unnamed = ampToolCall({ call: { id: 'TU-1', name: '', input: {} }, result: { toolUseId: 'TU-1', content: 'ok', isError: false }, finished: true })
    expect(unnamed.kind).toBe('mcp')
    expect(unnamed.title).toBe('Tool')
    expect(unnamed.label).toBeUndefined()
  })
})
