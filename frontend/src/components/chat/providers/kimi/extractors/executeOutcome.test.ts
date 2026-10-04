import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { kimiToolResult, kimiToolStart } from '~/test-support/kimiFixtures'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import { input } from '../../testUtils'
import '../../index'

function command(output: string, failed: boolean) {
  const call = providerToolCall(AgentProvider.KIMI_CODE, kimiToolResult('native-call', output, { isError: failed }), {
    request: input(kimiToolStart('native-call', 'Bash', { command: 'run the native command' }), undefined, AgentProvider.KIMI_CODE),
    spanType: 'Bash',
  })
  if (!call || call.kind !== 'execute')
    throw new Error('The native Kimi command requires an execute result.')
  return { call, result: typedResult(call)?.commands[0] }
}

describe('kimi native command outcome', () => {
  it('keeps successful stdout that resembles a native error trailer', () => {
    const output = 'Printed diagnostic\nCommand failed with exit code: 7.'
    const value = command(output, false)
    expect(value.call.status).toBe('completed')
    expect(value.result).toMatchObject({ output, exitCode: 0 })
  })

  it('reads the native failed exit code before the foreground task metadata', () => {
    const output = 'Native stderr\nCommand failed with exit code: 7.\ntask_id: bash-n4hig35g\noutput_size_bytes: 14\nnext_step: Use TaskOutput(task_id="bash-n4hig35g") to query the task output.'
    const value = command(output, true)
    expect(value.call.status).toBe('failed')
    expect(value.result?.exitCode).toBe(7)
  })
})
