import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMiMoShellToolExecution, mimoShellOutcome } from './shellToolExecution'

const shared = vi.hoisted(() => ({ exerciseShellToolExecution: vi.fn() }))
vi.mock('../helpers/nativeToolExecution', async (importOriginal) => {
  const original = await importOriginal<typeof import('../helpers/nativeToolExecution')>()
  return { ...original, exerciseShellToolExecution: shared.exerciseShellToolExecution }
})

const context = { provider: AgentProvider.MIMO_CODE } as ManagedNativeScenarioContext

beforeEach(() => {
  vi.clearAllMocks()
  shared.exerciseShellToolExecution.mockResolvedValue(undefined)
})

describe('exerciseMiMoShellToolExecution', () => {
  it('holds each command until its output shows, and reads the exit through the MiMo reader', async () => {
    await exerciseMiMoShellToolExecution(context)
    expect(shared.exerciseShellToolExecution).toHaveBeenCalledWith({ ...context, readToolResult: expect.any(Function) }, { outputGate: true })
  })

  it('keeps the options of the caller beside the output gate', async () => {
    const prepare = vi.fn(async () => {})
    await exerciseMiMoShellToolExecution(context, { includeFailure: false, prepare })
    expect(shared.exerciseShellToolExecution).toHaveBeenCalledWith({ ...context, readToolResult: expect.any(Function) }, { includeFailure: false, prepare, outputGate: true })
  })

  it('returns after the shared helper completes and rethrows its failure', async () => {
    const failure = new Error('the shell scenario failed')
    shared.exerciseShellToolExecution.mockRejectedValue(failure)
    await expect(exerciseMiMoShellToolExecution(context)).rejects.toBe(failure)
  })
})

/** A stored Worker message of the native session that holds `frame`. */
function stored(frame: unknown, spanId = 'shell-part', agentSessionId = 'native-session'): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: `message-${Math.random()}`,
    content: new TextEncoder().encode(JSON.stringify(frame)),
    contentCompression: ContentCompression.NONE,
    spanId,
    agentSessionId,
  })
}

/** Verbatim shape of a completed MiMo Code 0.1.15 `bash` part. */
function bashPart(exit: unknown, output = 'SHELLERR77\n', status = 'completed', callID = 'shell-call') {
  return {
    type: 'message.part.updated',
    properties: { part: { id: 'shell-part', sessionID: 'native-session', messageID: 'native-message', type: 'tool', tool: 'bash', callID, state: { status, input: { command: 'printf x >&2; exit 7' }, output, metadata: { output, exit, truncated: false } } } },
  }
}

function snapshot(...messages: AgentChatMessage[]): NativeMessageSnapshot {
  return { agentId: 'agent', agentSessionId: 'native-session', messages }
}

describe('mimoShellOutcome', () => {
  it('reads the exit of the completed part, and keeps the output of the model request', () => {
    expect(mimoShellOutcome(snapshot(stored(bashPart(7))), 'shell-call', 'SHELLERR77\n')).toEqual({ text: 'SHELLERR77\n', exitCode: 7, failed: true })
  })

  it('reads a zero exit as a command that did not fail', () => {
    expect(mimoShellOutcome(snapshot(stored(bashPart(0, 'out\n'))), 'shell-call', 'out\n')).toEqual({ text: 'out\n', exitCode: 0, failed: false })
  })

  it('reads the completed part among the running parts and the frames of other calls and sessions', () => {
    const outcome = mimoShellOutcome(snapshot(
      stored(bashPart(undefined, '', 'running')),
      stored(bashPart(3, 'other\n', 'completed', 'other-call'), 'other-call'),
      stored(bashPart(5), 'shell-call', 'another-session'),
      stored(bashPart(7)),
    ), 'shell-call', 'SHELLERR77\n')
    expect(outcome.exitCode).toBe(7)
  })

  it.each([
    ['no completed part', snapshot(stored(bashPart(7, 'SHELLERR77\n', 'running'))), 'one completed part'],
    ['two completed parts', snapshot(stored(bashPart(7)), stored(bashPart(7))), 'one completed part'],
    ['a Worker span that uses the model CallID', snapshot(stored(bashPart(7), 'shell-call')), 'one completed part'],
    ['an exit that is not an integer', snapshot(stored(bashPart('7'))), 'safe integer exit code'],
    ['another output than the model request', snapshot(stored(bashPart(7, 'different\n'))), 'different output bytes'],
  ])('refuses %s', (_case, read, message) => {
    expect(() => mimoShellOutcome(read, 'shell-call', 'SHELLERR77\n')).toThrow(message)
  })

  it('refuses an empty call ID before it reads a frame', () => {
    expect(() => mimoShellOutcome(snapshot(stored(bashPart(7))), '', 'SHELLERR77\n')).toThrow('exact call ID')
  })
})
