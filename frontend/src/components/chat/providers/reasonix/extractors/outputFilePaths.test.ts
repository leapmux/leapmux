import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { outputFilePathFixture } from '~/test-support/outputFilePathFixture'
import { providerToolCall } from '~/test-support/toolCallFixture'
import '../plugin'

const path = '/native/reasonix/session.outputs/native-call.txt'
const receipt = `[bash output kept out of context: 6001 lines, 632 KB]\nFull output: ${path}\nRead a window of it with read_file (offset/limit), or search it with grep. It is not in this conversation.\n\nOpening lines:\nfirst output line\n`
const frame = { sessionUpdate: 'tool_call_update', toolCallId: 'native-call', status: 'completed', kind: 'execute', title: 'bash', rawInput: { command: 'generate' }, content: [{ type: 'content', content: { type: 'text', text: receipt } }] }
const options = { spanId: 'native-call', spanType: 'bash', agentSessionId: 'session' }

describe('reasonixOutputFilePaths', () => {
  it('requires the worker witness when a failed receipt has no opening preview', () => {
    const headline = 'x'.repeat(1024)
    const native = { ...frame, status: 'failed', content: [{ type: 'content', content: { type: 'text', text: headline } }] }
    const stored = `${receipt.split('\n\nOpening lines:')[0]}\n`
    const supplementalContent = {
      sessionUpdate: native.sessionUpdate,
      toolCallId: native.toolCallId,
      status: native.status,
      rawOutput: { reasonix: { role: 'tool', tool_call_id: native.toolCallId, name: 'bash', content: stored }, reasonixOutputReceipt: { path, errorHeadline: headline } },
    }
    expect(providerToolCall(AgentProvider.REASONIX, native, { ...options, supplementalContent })?.outputFilePaths).toEqual([path])
    supplementalContent.rawOutput.reasonixOutputReceipt.errorHeadline = 'another error'
    const refused = providerToolCall(AgentProvider.REASONIX, native, { ...options, supplementalContent })
    expect(refused?.outputFilePaths).toBeUndefined()
    expect(refused?.kind === 'execute' ? refused.result : null).toMatchObject({ commands: [{ output: headline }] })
  })

  it.each(['exit status 7', 'command exited: exit status 7'])('reads a retained failed spill and keeps the %s preview', (headline) => {
    const native = { ...frame, status: 'failed', content: [{ type: 'content', content: { type: 'text', text: headline } }] }
    const stored = receipt.replace('first output line', `error: ${headline}\nfirst output line`)
    const supplementalContent = {
      sessionUpdate: native.sessionUpdate,
      toolCallId: native.toolCallId,
      status: native.status,
      rawOutput: { reasonix: { role: 'tool', tool_call_id: native.toolCallId, name: 'bash', content: stored } },
    }
    const call = providerToolCall(AgentProvider.REASONIX, native, { ...options, supplementalContent })
    expect(call?.outputFilePaths).toEqual([path])
    expect(call?.kind === 'execute' ? call.result : null).toMatchObject({ commands: [{ output: headline }] })
  })

  it.each(['foreign identity', 'foreign error', 'foreign status', 'missing preview'])('rejects a retained failed receipt with %s', (invalid) => {
    const native = { ...frame, status: 'failed', content: [{ type: 'content', content: { type: 'text', text: 'exit status 7' } }] }
    const stored = receipt.replace('first output line', 'error: exit status 7\nfirst output line')
    const supplementalContent = {
      sessionUpdate: native.sessionUpdate,
      toolCallId: invalid === 'foreign identity' ? 'other' : native.toolCallId,
      status: invalid === 'foreign status' ? 'completed' : native.status,
      rawOutput: { reasonix: { role: 'tool', tool_call_id: native.toolCallId, name: 'bash', content: invalid === 'foreign error' ? stored.replace('exit status 7', 'exit status 1') : invalid === 'missing preview' ? receipt : stored } },
    }
    expect(providerToolCall(AgentProvider.REASONIX, native, { ...options, supplementalContent })?.outputFilePaths).toBeUndefined()
  })

  it('binds a capability-wrapped result to the original native tool name', () => {
    const wrapped = {
      ...frame,
      title: 'use_capability',
      rawInput: { action: 'call', capability_id: 'tool:bash', arguments: { command: 'generate' } },
      content: [{ type: 'content', content: { type: 'text', text: receipt.replace('[bash ', '[use_capability ') } }],
    }
    expect(providerToolCall(AgentProvider.REASONIX, wrapped, options)?.outputFilePaths).toEqual([path])
  })

  it.each(['completed', 'failed'])('keeps the %s native frame unchanged and exposes its file', (status) => {
    const native = { ...frame, status }
    const before = JSON.stringify(native)
    expect(providerToolCall(AgentProvider.REASONIX, native, options)?.outputFilePaths).toEqual([path])
    expect(JSON.stringify(native)).toBe(before)
  })

  it.each(['request', 'none', 'other'] as const)('rejects the %s row role', (role) => {
    expect(outputFilePathFixture(AgentProvider.REASONIX, frame, options)(frame, { role })).toEqual([])
  })

  it.each([null, false, {}, { ...frame, toolCallId: 'other' }, { ...frame, status: 'in_progress' }, { ...frame, content: [...frame.content, ...frame.content] }])('rejects a malformed, unfinished, foreign, or ambiguous result: %j', (value) => {
    expect(outputFilePathFixture(AgentProvider.REASONIX, frame, options)(value)).toEqual([])
  })
})
