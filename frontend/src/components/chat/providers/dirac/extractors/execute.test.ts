import type { ToolCallSpec } from '../../../model/toolCall'
import { describe, expect, it, vi } from 'vitest'
import { failedResult } from '../../../model/toolCall'
import { acpToolFacts } from '../../acp/extractors/toolCall'
import { diracCommandExit, diracExecuteSpec } from './execute'

function nativeFacts(rawOutput: unknown = { output: 'Native preview.', exitCode: 7 }) {
  return acpToolFacts({
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-call',
    kind: 'execute',
    name: 'execute_command',
    status: 'completed',
    rawInput: { command: 'node native-script.js' },
    rawOutput,
  })
}

function sharedSpec(): ToolCallSpec {
  return {
    kind: 'execute',
    request: { command: 'node native-script.js', description: 'Native command' },
    title: 'Native title',
    metadata: [{ label: 'Native metadata', value: 'kept' }],
    result: {
      commands: [{ output: 'Native preview.', exitCode: 37, label: 'inline', durationMs: 5, truncated: true, outputUnavailable: false }],
      unresolvedTerminals: [],
    },
  }
}

describe('diracCommandExit', () => {
  it.each([0, 7, -7, Number.MAX_SAFE_INTEGER, Number.MIN_SAFE_INTEGER])('preserves a safe signed native code %s', (exitCode) => {
    expect(diracCommandExit({ rawOutput: { output: 'Native preview.', exitCode, signal: null } })).toEqual({ exitCode })
  })

  it('keeps an explicit null code without claiming an exit', () => {
    expect(diracCommandExit({ rawOutput: { output: '', exitCode: null, signal: null } })).toEqual({ exitCode: null })
  })

  it.each([undefined, '', '7', false, 1.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 1, {}, []])('claims no numeric code for a missing or malformed field: %j', (exitCode) => {
    expect(diracCommandExit({ rawOutput: { output: '', ...(exitCode === undefined ? {} : { exitCode }) } })).toEqual({})
  })

  it.each([null, 0, 7])('preserves a native signal before a conflicting code: %j', (exitCode) => {
    expect(diracCommandExit({ rawOutput: { output: 'Native preview.', exitCode, signal: 'SIGTERM' } })).toEqual({ signal: 'SIGTERM' })
  })

  it.each([undefined, null, '', ' ', false, 7, [], {}, 'SIG\0TERM'])('claims no signal for an absent or malformed field: %j', (signal) => {
    expect(diracCommandExit({ rawOutput: { output: '', exitCode: null, signal } })).toEqual({ exitCode: null })
  })

  it('keeps a rejected command without a numeric or signal claim', () => {
    expect(diracCommandExit({ rawOutput: { output: '', userRejected: true, exitCode: 0, signal: 'SIGTERM' } })).toEqual({})
  })

  it.each([undefined, null, false, 0, '', [], {}, { exitCode: 7 }, { output: false, exitCode: 7 }])('does not read exit fields from another raw shape: %j', (rawOutput) => {
    expect(diracCommandExit({ rawOutput })).toBeUndefined()
  })
})

describe('diracExecuteSpec', () => {
  it.each([
    [0, 'Command executed successfully (exit code 0).\nOutput:\nSHELL42', 'SHELL42'],
    [7, 'Command failed with exit code 7.\nOutput:\nSHELLERR77', 'SHELLERR77'],
  ])('removes the native model preamble for exit %s', (exitCode, output, expected) => {
    const spec = sharedSpec()
    if (spec.kind !== 'execute' || !spec.result || !('commands' in spec.result))
      throw new Error('The fixture requires one command.')
    spec.result.commands[0]!.output = output
    const result = diracExecuteSpec(nativeFacts({ output, exitCode }), () => spec)
    expect(result).toMatchObject({ result: { commands: [{ output: expected, exitCode }] } })
  })

  it('changes only the native exit and keeps the original result fields', () => {
    const facts = nativeFacts()
    const spec = sharedSpec()
    const before = structuredClone(spec)
    const base = vi.fn(() => spec)
    const result = diracExecuteSpec(facts, base)
    expect(base).toHaveBeenCalledTimes(1)
    expect(result).toEqual({
      ...before,
      result: {
        commands: [{ output: 'Native preview.', exitCode: 7, label: 'inline', durationMs: 5, truncated: true, outputUnavailable: false }],
        unresolvedTerminals: [],
      },
    })
    expect(spec).toEqual(before)
  })

  it('removes a stale numeric exit before preserving the native signal', () => {
    const result = diracExecuteSpec(nativeFacts({ output: 'Native preview.', exitCode: 0, signal: 'SIGTERM' }), sharedSpec)
    expect(result.kind).toBe('execute')
    if (result.kind !== 'execute' || !result.result || !('commands' in result.result))
      throw new Error('The native signal result requires one command.')
    expect(result.result.commands[0]?.signal).toBe('SIGTERM')
    expect(result.result.commands[0]?.exitCode).toBeUndefined()
    expect(result.result.commands[0]?.output).toBe('Native preview.')
  })

  it('preserves multiple commands without applying an aggregate exit to them', () => {
    const spec: ToolCallSpec = { kind: 'execute', request: { command: 'native' }, result: { commands: [{ output: 'one', exitCode: 1 }, { output: 'two', signal: 'SIGINT' }], unresolvedTerminals: [] } }
    expect(diracExecuteSpec(nativeFacts(), () => spec)).toBe(spec)
  })

  it('preserves resolved terminal command ownership', () => {
    const facts = nativeFacts()
    facts.terminals.entries.push({ output: 'Native preview.', exitCode: 3 })
    const spec = sharedSpec()
    expect(diracExecuteSpec(facts, () => spec)).toBe(spec)
  })

  it.each(['facts', 'result'] as const)('keeps unresolved terminal references in the %s', (owner) => {
    const facts = nativeFacts()
    const spec: ToolCallSpec = { kind: 'execute', request: { command: 'native' }, result: { commands: [{ output: 'Native preview.' }], unresolvedTerminals: owner === 'result' ? ['native-terminal'] : [] } }
    if (owner === 'facts')
      facts.terminals.unresolved.push('native-terminal')
    expect(diracExecuteSpec(facts, () => spec)).toBe(spec)
  })

  it.each([
    { kind: 'execute', request: { command: 'native' } },
    { kind: 'execute', request: { command: 'native' }, result: { commands: [], unresolvedTerminals: [] } },
    { kind: 'execute', request: { command: 'native' }, result: failedResult('Native failure.') },
    { kind: 'read', request: { path: '/native/file' } },
  ] satisfies ToolCallSpec[])('preserves a result without one inline command: %j', (spec) => {
    expect(diracExecuteSpec(nativeFacts(), () => spec)).toBe(spec)
  })

  it('keeps the shared result when the raw output has no native command body', () => {
    const spec = sharedSpec()
    expect(diracExecuteSpec(nativeFacts({ exitCode: 7 }), () => spec)).toBe(spec)
  })
})
