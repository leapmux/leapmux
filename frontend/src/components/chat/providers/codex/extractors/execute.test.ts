import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerRow } from '~/test-support/toolCallFixture'
import { input } from '../../testUtils'
import { codexCommandActionsFromItem, codexCommandFromItem, codexRawExecRole, codexUnwrapCommand } from './execute'
import { nativeExecFailureRequest, nativeExecFailureResult, nativeExecSuccessRequest, nativeExecSuccessResult } from './execute.fixtures'
import '../plugin'

const rawExecSource = 'text("Native computed output: " + (40 + 2));'
const rawExecRequest = { threadId: 'native-thread', turnId: 'native-turn', item: { type: 'custom_tool_call', call_id: 'native-exec-call', name: 'exec', input: rawExecSource, status: 'completed' } }
const rawExecResult = (output: unknown) => ({ threadId: 'native-thread', turnId: 'native-turn', item: { type: 'custom_tool_call_output', call_id: 'native-exec-call', output } })

function rawExecRow(output: unknown, request = rawExecRequest, extra: { supplementalContent?: unknown, completion?: MessageCompletion } = {}) {
  return providerRow(AgentProvider.CODEX, rawExecResult(output), { category: { kind: 'tool_use' }, role: 'result', spanType: 'exec', request: input(request, undefined, AgentProvider.CODEX), ...extra })
}

const cellHeader = (status: string) => ({ type: 'input_text', text: `${status}\nWall time 1.0 seconds\nOutput:\n` })
const cellText = (text: string) => ({ type: 'input_text', text })
const cellYield = 'Script running with cell ID 7'

/** The supplement the worker stores beside the exec output of a cell that yielded. */
function cellSupplement(...continuationOutputs: unknown[]) {
  return { callId: 'native-exec-call', continuationOutputs }
}

describe('codex native exec extraction', () => {
  it('reports the native unsupported execution call as failed', () => {
    const output = 'unsupported custom tool call: exec'
    expect(rawExecRow(output)).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'failed', result: { commands: [{ output, failed: true }] } } })
  })

  it.each([
    'prefix unsupported custom tool call: exec',
    'unsupported custom tool call: exec suffix',
    'unsupported custom tool call: exec\n',
    'unsupported custom tool call: another_tool',
  ])('preserves an unknown dispatch message as text: %j', (output) => {
    expect(rawExecRow(output)).toMatchObject({ kind: 'tool', call: { result: { unparsed: true, text: output } } })
  })

  it('keeps printed dispatch errors separate from native failure', () => {
    const output = 'unsupported custom tool call: exec'
    expect(rawExecRow([cellHeader('Script completed'), cellText(output)])).toMatchObject({ kind: 'tool', call: { status: 'completed', result: { commands: [{ output }] } } })
    expect(rawExecRow([cellText(output)])).toMatchObject({ kind: 'tool', call: { result: { unparsed: true, text: output } } })
  })

  it.each([0.1, 0.3, 1234567890.1])('retains native cancellation after %s seconds', (seconds) => {
    const output = `aborted by user after ${seconds.toFixed(1)}s`
    expect(rawExecRow(output)).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'cancelled', result: { commands: [{ output, durationMs: seconds * 1000 }] } } })
  })

  it.each([
    '',
    'prefix aborted by user after 0.3s',
    'aborted by user after 0.3s suffix',
    'aborted by user after 0.3s\n',
    'aborted by user after -0.3s',
    'aborted by user after 0.0s',
    'aborted by user after 3s',
    'aborted by user after 0.30s',
    `aborted by user after ${'9'.repeat(400)}.1s`,
    `aborted by user after 1${'0'.repeat(306)}.1s`,
  ])('preserves a malformed cancellation as unknown text: %j', (output) => {
    expect(rawExecRow(output)).toMatchObject({ kind: 'tool', call: { kind: 'execute', result: { unparsed: true, text: output } } })
  })

  it('keeps a printed cancellation inside the script body as ordinary output', () => {
    const output = 'aborted by user after 0.3s'
    expect(rawExecRow([cellHeader('Script completed'), cellText(output)])).toMatchObject({ kind: 'tool', call: { status: 'completed', result: { commands: [{ output }] } } })
    expect(rawExecRow([cellText(output)])).toMatchObject({ kind: 'tool', call: { result: { unparsed: true, text: output } } })
  })

  it('retains earlier output when a native cancellation ends a yielded cell', () => {
    const row = rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement('aborted by user after 0.3s'),
    })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'cancelled', result: { commands: [{ output: 'partial\naborted by user after 0.3s' }] } } })
    if (row?.kind !== 'tool' || row.call.kind !== 'execute' || !row.call.result || !('commands' in row.call.result))
      throw new Error('The cancelled cell lost its output.')
    expect(row.call.result.commands[0]).not.toHaveProperty('durationMs')
  })

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

describe('codex code cell continuation', () => {
  it('joins the output of every yield and wait into one card with the final status', () => {
    const row = rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement([cellHeader(cellYield), cellText('more')], [cellHeader('Script completed'), cellText('final')]),
    })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed', result: { commands: [{ output: 'partial\nmore\nfinal' }] } } })
    if (row?.kind !== 'tool' || row.call.kind !== 'execute' || !row.call.result || !('commands' in row.call.result))
      throw new Error('The joined cell lost its execute result.')
    // The cell ran across several calls, so no one wall time states its duration.
    expect(row.call.result.commands[0]).not.toHaveProperty('durationMs')
  })

  it('reads a wait output in the native string variant', () => {
    expect(rawExecRow([cellHeader(cellYield)], rawExecRequest, {
      supplementalContent: cellSupplement('Script completed\nWall time 0.5 seconds\nOutput:\nfinal'),
    })).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed', result: { commands: [{ output: 'final' }] } } })
  })

  it('reports a cell whose last wait failed', () => {
    expect(rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement([cellHeader('Script failed'), cellText('Script error:\nError: 77')]),
    })).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'failed', result: { commands: [{ output: 'partial\nScript error:\nError: 77', failed: true }] } } })
  })

  it('reports a cell that a wait terminated as cancelled', () => {
    expect(rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement([cellHeader('Script terminated')]),
    })).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'cancelled', result: { commands: [{ output: 'partial' }] } } })
  })

  it('reports a cell that still ran at the turn end as cancelled, with the output it stated', () => {
    expect(rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, { completion: MessageCompletion.INTERRUPTED }))
      .toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'cancelled', result: { commands: [{ output: 'partial' }] } } })
  })

  it('takes the status from the last header, never from text that the script printed', () => {
    expect(rawExecRow([cellHeader(cellYield), cellText('Script failed\nA printed status-like value')], rawExecRequest, {
      supplementalContent: cellSupplement([cellHeader('Script completed'), cellText('done')]),
    })).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed' } })
  })

  it('collects the images of every output', () => {
    const image = { type: 'input_image', image_url: 'data:image/png;base64,AAAA' }
    const row = rawExecRow([cellHeader(cellYield), image], rawExecRequest, {
      supplementalContent: cellSupplement([cellHeader('Script completed'), image]),
    })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', images: [expect.anything(), expect.anything()] } })
  })

  it('skips a continuation value that is neither text nor a list', () => {
    expect(rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement(null, 3, [cellHeader('Script completed'), cellText('final')]),
    })).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed', result: { commands: [{ output: 'partial\nfinal' }] } } })
  })

  it('retains every output as unparsed text when one header is not a native header', () => {
    const row = rawExecRow([cellHeader(cellYield), cellText('partial')], rawExecRequest, {
      supplementalContent: cellSupplement('A different native output format\n42'),
    })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', result: { unparsed: true, text: expect.stringContaining('A different native output format') } } })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', result: { text: expect.stringContaining('partial') } } })
  })

  it('ignores the supplement of another call', () => {
    const row = rawExecRow([cellHeader('Script completed'), cellText('own output')], rawExecRequest, {
      supplementalContent: { callId: 'another-call', continuationOutputs: [[cellHeader('Script failed'), cellText('foreign')]] },
    })
    expect(row).toMatchObject({ kind: 'tool', call: { kind: 'execute', status: 'completed', result: { commands: [{ output: 'own output' }] } } })
  })
})
