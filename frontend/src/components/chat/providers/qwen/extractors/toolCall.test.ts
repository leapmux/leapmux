import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { typedResult } from '../../../model/toolCall'
import '../plugin'

describe('qwenToolCallAdapter', () => {
  const owner = '97d857a0-9cd7-4bbf-987b-a6ab094a074b'
  const registryKey = 'qwen-registry-v1:a1177c0911fd2197b36567c453224ef3d6fb6c4b7368399506542fa820c9aae5'
  const spawn = {
    sessionUpdate: 'tool_call',
    toolCallId: 'call_shared',
    status: 'pending',
    kind: 'other',
    rawInput: { description: 'Stored child', prompt: 'Read the stored task.' },
    _meta: { toolName: 'agent' },
  }

  it('derives the child link from the stored row owner', () => {
    const call = providerToolCall(AgentProvider.QWEN_CODE, spawn, { role: 'request', agentSessionId: owner })
    expect(call?.kind).toBe('agent')
    expect(call?.request).toMatchObject({ description: 'Stored child', prompt: 'Read the stored task.', registryKey })
  })

  it('keeps the stored owner when the result supplies a different session lookalike', () => {
    const call = providerToolCall(AgentProvider.QWEN_CODE, {
      ...spawn,
      sessionUpdate: 'tool_call_update',
      sessionId: '654fbfe5-9769-4517-8dfe-fcfbf19d61ac',
      status: 'completed',
      rawOutput: { type: 'task_execution', executionMode: 'foreground', status: 'completed', result: 'The stored task is done.' },
    }, { role: 'result', agentSessionId: owner })
    expect(call?.request).toMatchObject({ registryKey })
    const result = call && typedResult(call)
    expect(result).toMatchObject({ agents: [{ registryKey, body: 'The stored task is done.' }] })
  })

  it('keeps the child card without guessing an absent stored owner', () => {
    const call = providerToolCall(AgentProvider.QWEN_CODE, spawn, { role: 'request' })
    expect(call?.request).toMatchObject({ description: 'Stored child', prompt: 'Read the stored task.' })
    expect(call?.request).not.toHaveProperty('registryKey')
  })
})

/**
 * Verbatim shape of a Qwen Code result of `run_shell_command`: the content is the record
 * that Qwen gives its model, and `rawOutput` states the output and the exit apart.
 */
function shellUpdate(status: 'completed' | 'failed', exit: { exitCode: number | null, signal: string | null }): Record<string, unknown> {
  const output = status === 'failed' ? 'SHELLERR77' : 'SHELL42'
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: 'native-shell',
    status,
    kind: 'execute',
    rawInput: { command: 'printf x' },
    content: [{ type: 'content', content: { type: 'text', text: `Command: printf x\nDirectory: (root)\nOutput: ${output}\nError: (none)\nExit Code: ${exit.exitCode ?? '(none)'}\nSignal: ${exit.signal ?? '(none)'}\nProcess Group PGID: 14134` } }],
    _meta: { toolName: 'run_shell_command', provenance: 'builtin' },
    rawOutput: { type: 'shell_result', version: 1, text: output, output, directory: '/work', ...exit, pid: 14134, error: null, outcome: status, notices: [], truncated: false, outputFiles: [] },
  }
}

function command(frame: Record<string, unknown>) {
  const call = providerToolCall(AgentProvider.QWEN_CODE, frame, { spanType: 'execute', role: 'result' })
  const result = call && typedResult(call)
  if (!result || !('commands' in result))
    throw new Error('The native shell result must remain a command result.')
  return { status: call.status, command: result.commands[0] }
}

describe('a Qwen Code shell result', () => {
  it('reads the output and the code of a completed command from its record', () => {
    expect(command(shellUpdate('completed', { exitCode: 0, signal: null })).command).toMatchObject({ output: 'SHELL42', exitCode: 0 })
  })

  it('states the failed exit and reads the output without the model record', () => {
    const frame = shellUpdate('failed', { exitCode: 7, signal: null })
    const failed = command(frame)
    expect(failed.status).toBe('failed')
    expect(failed.command).toMatchObject({ output: 'SHELLERR77', exitCode: 7 })
  })

  it('states the signal of a failed command that a signal ended', () => {
    expect(command(shellUpdate('failed', { exitCode: null, signal: 'SIGKILL' })).command).toMatchObject({ signal: 'SIGKILL' })
  })

  it('states no code for a failed update that carries no shell record', () => {
    const frame = { ...shellUpdate('failed', { exitCode: 7, signal: null }), rawOutput: undefined }
    expect(command(frame).command).not.toHaveProperty('exitCode', 7)
  })

  it.each([7.5, Number.MAX_SAFE_INTEGER + 1])('states no exit for the invalid native code %s', (exitCode) => {
    const result = command(shellUpdate('failed', { exitCode, signal: null })).command
    expect(result).not.toHaveProperty('exitCode')
    expect(result?.output).toBe('SHELLERR77')
  })

  it('keeps the native signal when the code is invalid', () => {
    const result = command(shellUpdate('failed', { exitCode: 7.5, signal: 'SIGKILL' })).command
    expect(result).toMatchObject({ output: 'SHELLERR77', signal: 'SIGKILL' })
    expect(result).not.toHaveProperty('exitCode')
  })
})
