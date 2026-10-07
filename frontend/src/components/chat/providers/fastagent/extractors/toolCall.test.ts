import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { fastAgentCommandExit } from './toolCall'
import '../plugin'

/**
 * Verbatim shape of a Fast Agent result for `printf 'SHELLERR%s\n' 77 >&2; exit 7`. Fast
 * Agent runs the command in a LeapMux client terminal and writes the result text itself
 * (`TerminalRuntime._format_result_text`): the output, an optional
 * `[Terminated by signal: S]` block, and the `[Exit code: N]` block. The stored
 * supplement adds `kind: execute` to the update; the fixture states it directly.
 */
function nativeFrame(text: string, status = 'failed'): Record<string, unknown> {
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-call',
    status,
    kind: 'execute',
    title: 'execute',
    content: [{ type: 'content', content: { type: 'text', text } }],
    rawInput: { command: 'printf x >&2; exit 7' },
    rawOutput: text,
  }
}

describe('fastAgentCommandExit', () => {
  it('reads the code of the final exit block', () => {
    expect(fastAgentCommandExit('SHELLERR77\n\n\n[Exit code: 7]')).toEqual({ exitCode: 7 })
  })

  it('reads the zero code of a command that succeeded', () => {
    expect(fastAgentCommandExit('out\n\n[Exit code: 0]')).toEqual({ exitCode: 0 })
  })

  it('reads a code after a signal block and after the truncation notice', () => {
    expect(fastAgentCommandExit('[Output truncated by ACP terminal outputByteLimit: 10 bytes (~2 tokens). Client returned partial output only.]\nout\n\n[Terminated by signal: SIGKILL]\n\n[Exit code: -9]')).toEqual({ exitCode: -9 })
  })

  it('reads only the final block, and leaves the same words inside the output alone', () => {
    expect(fastAgentCommandExit('[Exit code: 3]\nprinted\n\n[Exit code: 0]')).toEqual({ exitCode: 0 })
  })

  it.each([
    ['a text without the block', 'out'],
    ['a block that is not the last line', '[Exit code: 7]\nout'],
    ['a code that is not an integer', 'out\n\n[Exit code: x]'],
    ['an empty text', ''],
  ])('states no exit for %s', (_case, text) => {
    expect(fastAgentCommandExit(text)).toBeUndefined()
  })
})

describe('the Fast Agent command row', () => {
  it('states the exit code and removes the native exit block from the output', () => {
    const text = 'SHELLERR77\n\n\n[Exit code: 7]'
    const call = providerToolCall(AgentProvider.FAST_AGENT, nativeFrame(text), { spanType: 'execute', role: 'result' })
    expect(call?.status).toBe('failed')
    expect(call && typedResult(call)).toEqual({ commands: [expect.objectContaining({ output: 'SHELLERR77\n', exitCode: 7 })], unresolvedTerminals: [] })
  })
})
