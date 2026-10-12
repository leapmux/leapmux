import type { ToolCallSpec } from '../../../model/toolCall'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { acpToolFacts } from '../../acp/extractors/toolCall'
import { junieCommandExit, junieExecuteSpec } from './execute'
import '../plugin'

/**
 * Verbatim shape of a Junie result for `printf 'SHELLERR%s\n' 77 >&2; exit 7`: the
 * output and the code beside it, and the terminal exit in `_meta`.
 */
function nativeFrame(change: { rawOutput?: unknown, meta?: unknown, status?: string } = {}): Record<string, unknown> {
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-call',
    title: 'printf x >&2; exit 7',
    kind: 'execute',
    status: change.status ?? 'failed',
    content: [],
    locations: [],
    rawInput: { command: 'printf x >&2; exit 7', cwd: '/work' },
    rawOutput: 'rawOutput' in change ? change.rawOutput : { output: 'SHELLERR77', exitCode: 7 },
    ...('meta' in change ? (change.meta === undefined ? {} : { _meta: change.meta }) : { _meta: { terminal_exit: { exit_code: 7, signal: null, terminal_id: 'native-call' } } }),
  }
}

describe('junieCommandExit', () => {
  it('reads the code of the terminal exit', () => {
    expect(junieCommandExit(nativeFrame())).toEqual({ exitCode: 7 })
  })

  it('reads the signal of the terminal exit when it states no code', () => {
    expect(junieCommandExit(nativeFrame({ meta: { terminal_exit: { exit_code: null, signal: 'SIGKILL', terminal_id: 'native-call' } } }))).toEqual({ signal: 'SIGKILL' })
  })

  it('reads the code beside the output when the frame states no terminal exit', () => {
    expect(junieCommandExit(nativeFrame({ meta: undefined }))).toEqual({ exitCode: 7 })
  })

  it.each([
    ['no exit at all', { rawOutput: { output: 'x' }, meta: undefined }],
    ['a code that is not an integer', { rawOutput: { output: 'x', exitCode: 7.5 }, meta: { terminal_exit: { exit_code: '7' } } }],
    ['an output that is not an object', { rawOutput: 'x', meta: undefined }],
  ])('states no exit for %s', (_case, change) => {
    expect(junieCommandExit(nativeFrame(change))).toBeUndefined()
  })
})

describe('junieExecuteSpec', () => {
  it('adds the native exit to the one inline command, and keeps the rest of the shared result', () => {
    const shared: ToolCallSpec = { kind: 'execute', request: { command: 'printf x >&2; exit 7' }, result: { commands: [{ output: 'SHELLERR77', durationMs: 4 }], unresolvedTerminals: [] } }
    expect(junieExecuteSpec(acpToolFacts(nativeFrame()), () => shared)).toEqual({ ...shared, result: { commands: [{ output: 'SHELLERR77', durationMs: 4, exitCode: 7 }], unresolvedTerminals: [] } })
  })

  it('keeps a shared result that is not one inline command', () => {
    const shared: ToolCallSpec = { kind: 'execute', request: { command: 'x' }, result: { commands: [{ output: 'a' }, { output: 'b' }], unresolvedTerminals: [] } }
    expect(junieExecuteSpec(acpToolFacts(nativeFrame()), () => shared)).toBe(shared)
  })
})

describe('the Junie command row', () => {
  it('states the exit code of a failed command', () => {
    const call = providerToolCall(AgentProvider.JUNIE, nativeFrame(), { spanType: 'execute', role: 'result' })
    expect(call?.status).toBe('failed')
    expect(call && typedResult(call)).toEqual({ commands: [expect.objectContaining({ output: 'SHELLERR77', exitCode: 7 })], unresolvedTerminals: [] })
  })
})
