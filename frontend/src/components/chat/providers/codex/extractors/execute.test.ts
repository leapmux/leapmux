import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { input } from '../../testUtils'
import { codexCommandActionsFromItem, codexCommandFromItem, codexRawExecRole, codexUnwrapCommand } from './execute'
import { nativeExecFailureRequest, nativeExecFailureResult, nativeExecSuccessRequest, nativeExecSuccessResult } from './execute.fixtures'
import '../plugin'

const rawExecSource = 'text("Native computed output: " + (40 + 2));'
const rawExecRequest = { threadId: 'native-thread', turnId: 'native-turn', item: { type: 'custom_tool_call', call_id: 'native-exec-call', name: 'exec', input: rawExecSource, status: 'completed' } }
const rawExecResult = (output: unknown) => ({ threadId: 'native-thread', turnId: 'native-turn', item: { type: 'custom_tool_call_output', call_id: 'native-exec-call', output } })

function rawExecRow(output: unknown, request = rawExecRequest) {
  return providerRow(AgentProvider.CODEX, rawExecResult(output), { category: { kind: 'tool_use' }, role: 'result', spanType: 'exec', request: input(request, undefined, AgentProvider.CODEX) })
}

describe('codex native exec extraction', () => {
  it.each([
    { label: 'absent', namespace: undefined },
    { label: 'null', namespace: null },
    { label: 'empty', namespace: '' },
    { label: 'functions', namespace: 'functions' },
  ])('accepts the source-defined $label default execution namespace', ({ namespace }) => {
    expect(codexRawExecRole({ ...rawExecRequest, item: { ...rawExecRequest.item, namespace } })).toBe('request')
  })

  it('accepts an explicit empty script source', () => {
    expect(providerRow(AgentProvider.CODEX, { ...rawExecRequest, item: { ...rawExecRequest.item, input: '' } }, { category: { kind: 'tool_use' }, role: 'request', spanType: 'exec' })).toMatchObject({ kind: 'tool', role: 'request', call: { kind: 'execute', status: 'in_progress', request: { command: '', language: 'javascript' } } })
  })

  it.each([
    { label: 'unknown first line', output: 'A different native output format\n42' },
    { label: 'invalid timing', output: 'Script failed\nWall time unknown seconds\nOutput:\nError: 77' },
  ])('retains the $label as unparsed text without an invented script outcome', ({ output }) => {
    expect(rawExecRow(output)).toMatchObject({ kind: 'tool', call: { kind: 'execute', result: { unparsed: true, text: output } } })
  })

  it.each([
    { request: nativeExecSuccessRequest, result: nativeExecSuccessResult, expected: 'NATIVE_CODE_SUCCESS_42', status: 'completed' },
    { request: nativeExecFailureRequest, result: nativeExecFailureResult, expected: 'NATIVE_CODE_FAILURE', status: 'failed' },
  ])('reads the exact installed native $status notification pair', ({ request, result, expected, status }) => {
    const before = JSON.stringify({ request, result })
    const row = providerRow(AgentProvider.CODEX, result, { category: { kind: 'tool_use' }, role: 'result', spanType: 'exec', request: input(request, undefined, AgentProvider.CODEX) })
    expect(row).toMatchObject({ kind: 'tool', call: { id: request.params.item.call_id, kind: 'execute', status, request: { command: request.params.item.input, language: 'javascript' }, result: { commands: [{ output: expect.stringContaining(expected) }] } } })
    expect(JSON.stringify({ request, result })).toBe(before)
  })

  it('extracts the source and actual computed output from separate native items', () => {
    expect(rawExecRow([{ type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }, { type: 'input_text', text: 'Native computed output: 42' }])).toMatchObject({ kind: 'tool', role: 'result', call: { id: 'native-exec-call', kind: 'execute', status: 'completed', request: { command: rawExecSource, language: 'javascript' }, result: { commands: [{ output: 'Native computed output: 42', durationMs: 0 }] } } })
  })

  it('retains exact freeform source text before the script returns', () => {
    const source = '// A native comment\ntext("한글");\n'
    const request = { ...rawExecRequest, item: { ...rawExecRequest.item, input: source } }
    expect(providerRow(AgentProvider.CODEX, request, { category: { kind: 'tool_use' }, role: 'request', spanType: 'exec' })).toMatchObject({ kind: 'tool', role: 'request', call: { id: 'native-exec-call', kind: 'execute', status: 'in_progress', request: { command: source, language: 'javascript' } } })
  })

  it('reports script failure without inventing a process exit code', () => {
    const row = rawExecRow([{ type: 'input_text', text: 'Script failed\nWall time 0.0 seconds\nOutput:\n' }, { type: 'input_text', text: 'Script error:\nError: Native thrown value: 77' }])
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'failed', result: { commands: [{ output: expect.stringContaining('Native thrown value: 77'), failed: true }] } } })
    if (row?.kind !== 'tool' || row.call.kind !== 'execute' || !row.call.result || !('commands' in row.call.result))
      throw new Error('The native script failure lost its execute result.')
    expect(row.call.result.commands[0]).not.toHaveProperty('exitCode')
  })

  it('does not use script body text as the native failure header', () => {
    expect(rawExecRow([{ type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }, { type: 'input_text', text: 'Script failed\nA printed status-like value' }])).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed' } })
  })

  it('retains an empty completed script output', () => {
    expect(rawExecRow([{ type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }])).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed', result: { commands: [{ output: '', durationMs: 0 }] } } })
  })

  it('reads the native string output variant', () => {
    expect(rawExecRow('Script failed\nWall time 0.0 seconds\nOutput:\nError: 77')).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'failed', result: { commands: [{ output: expect.stringContaining('Error: 77') }] } } })
  })

  it.each([
    { label: 'call', request: { ...rawExecRequest, item: { ...rawExecRequest.item, call_id: 'another-call' } } },
    { label: 'thread', request: { ...rawExecRequest, threadId: 'another-thread' } },
    { label: 'turn', request: { ...rawExecRequest, turnId: 'another-turn' } },
    { label: 'tool', request: { ...rawExecRequest, item: { ...rawExecRequest.item, name: 'apply_patch' } } },
  ])('rejects source text from another $label', ({ request }) => {
    const row = rawExecRow([{ type: 'input_text', text: 'Script completed\nWall time 0.0 seconds\nOutput:\n' }], request)
    expect(row?.kind === 'tool' && row.call.kind === 'execute' && row.call.request.command === rawExecSource).toBe(false)
  })
})

describe('codexUnwrapCommand', () => {
  it('strips /bin/zsh -lc shell wrapper', () => {
    expect(codexUnwrapCommand('/bin/zsh -lc \'echo hi\'')).toBe('echo hi')
  })

  it('passes through unwrapped commands', () => {
    expect(codexUnwrapCommand('echo hi')).toBe('echo hi')
  })
})

describe('codexCommandFromItem', () => {
  it('returns null for non-commandExecution items', () => {
    expect(codexCommandFromItem(null)).toBeNull()
    expect(codexCommandFromItem({ type: 'agentMessage' })).toBeNull()
  })

  it('extracts the structured payload', () => {
    expect(codexCommandFromItem({
      type: 'commandExecution',
      command: 'echo hi',
      aggregatedOutput: 'hi',
      exitCode: 0,
      durationMs: 10,
      status: 'completed',
    })).toEqual({
      output: 'hi',
      exitCode: 0,
      durationMs: 10,
    })
  })

  it('marks isError when status=failed', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      status: 'failed',
    })
    expect(source).not.toBeNull()
  })

  it('carries a non-zero exit code for the shared label to read', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      aggregatedOutput: '',
      exitCode: 5,
      status: 'completed',
    })
    expect(source?.exitCode).toBe(5)
  })

  // A refused approval never started a process. The row carries the refusal in
  // its status word, and the source claims neither an error nor an exit code.
  it('carries no exit code for a refused approval', () => {
    const source = codexCommandFromItem({
      type: 'commandExecution',
      command: './deploy.sh production',
      aggregatedOutput: '',
      status: 'declined',
    })
    expect(source?.exitCode).toBeNull()
  })
})

describe('codexCommandActionsFromItem', () => {
  it('translates every current command action', () => {
    expect(codexCommandActionsFromItem({
      commandActions: [
        { type: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' },
        { type: 'listFiles', command: 'rg --files src', path: 'src' },
        { type: 'search', command: 'rg -n \'needle\' src', query: 'needle', path: 'src' },
        { type: 'unknown', command: 'npm run custom-task' },
      ],
    })).toEqual([
      { kind: 'read', command: 'sed -n \'1,5p\' src/main.ts', name: 'main.ts', path: '/repo/src/main.ts' },
      { kind: 'list', command: 'rg --files src', path: 'src' },
      { kind: 'search', command: 'rg -n \'needle\' src', query: 'needle', path: 'src' },
      { kind: 'unknown', command: 'npm run custom-task' },
    ])
  })

  it('keeps nullable properties absent and preserves a future type as unknown', () => {
    expect(codexCommandActionsFromItem({
      commandActions: [
        { type: 'listFiles', command: 'pwd', path: null },
        { type: 'search', command: 'rg --files', query: null, path: null },
        { type: 'futureAction', command: 'future --flag', detail: 'new' },
      ],
    })).toEqual([
      { kind: 'list', command: 'pwd' },
      { kind: 'search', command: 'rg --files' },
      { kind: 'unknown', command: 'future --flag' },
    ])
  })

  it('drops entries without a command and returns no actions for an invalid list', () => {
    expect(codexCommandActionsFromItem({ commandActions: [null, 'read', {}, { type: 'read', path: '/repo/a.ts' }] })).toEqual([])
    expect(codexCommandActionsFromItem({ commandActions: 'not-an-array' })).toEqual([])
    expect(codexCommandActionsFromItem({})).toEqual([])
  })
})
