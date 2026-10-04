import type { MessageInitShape } from '@bufbuild/protobuf'
import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import type { JunieOutputPathOwner } from './outputFilePathReadiness'
import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, ContentCompression, MessageCompletion, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { deferred } from '../../../src/test-support/async'
import { withCleanup } from '../helpers/cleanup'
import { junieOutputFilePathReady, waitForJunieOutputFilePaths } from './outputFilePathReadiness'
import * as outputReaders from './outputFilePaths'

const encoder = new TextEncoder()
const owner: JunieOutputPathOwner = {
  agentId: 'native-agent',
  sessionId: 'session-261004-125946-1qa3',
  callId: '290f3e3a-92e2-440a-b5cf-31c2719ec05f',
  command: 'node exact-native-command.js',
  workingDirectory: '/owned/project',
}
const taskId = 'task-261004-125947-1qa3'
const path = '/owned/junie/sessions/session-261004-125946-1qa3/task-261004-125947-1qa3/terminal-output/terminal-output-123.txt'

function completedFrame(output = 'ORIGINAL_NATIVE_PREVIEW') {
  return {
    sessionUpdate: 'tool_call_update',
    toolCallId: owner.callId,
    kind: 'execute',
    status: 'completed',
    content: [],
    rawInput: { command: owner.command, cwd: owner.workingDirectory },
    rawOutput: { output, exitCode: 0 },
    _meta: { terminal_exit: { terminal_id: owner.callId, exit_code: 0, signal: null } },
  }
}

function pointerReceipt() {
  return {
    sessionId: owner.sessionId,
    toolCallId: owner.callId,
    taskId,
    command: owner.command,
    cwd: owner.workingDirectory,
    path,
    exitCode: 0,
  }
}

function providerReceipt(pointer: unknown = pointerReceipt()) {
  return { sessionUpdate: 'tool_call_update', toolCallId: owner.callId, status: 'completed', outputFilePath: pointer }
}

function snapshot(
  frame: unknown = completedFrame(),
  supplement: unknown = { provider: providerReceipt() },
  overrides: MessageInitShape<typeof AgentChatMessageSchema> = {},
): NativeMessageSnapshot {
  return {
    agentId: owner.agentId,
    agentSessionId: owner.sessionId,
    messages: [create(AgentChatMessageSchema, {
      id: 'native-completed-row',
      seq: 1n,
      source: MessageSource.AGENT,
      agentSessionId: owner.sessionId,
      spanId: owner.callId,
      spanType: 'execute',
      completion: MessageCompletion.COMPLETE,
      content: encoder.encode(JSON.stringify(frame)),
      contentCompression: ContentCompression.NONE,
      supplementalContent: encoder.encode(JSON.stringify(supplement)),
      supplementalContentCompression: ContentCompression.NONE,
      ...overrides,
    })],
  }
}

function pendingSnapshot(): NativeMessageSnapshot {
  return snapshot({
    sessionUpdate: 'tool_call',
    toolCallId: owner.callId,
    kind: 'execute',
    status: 'in_progress',
    content: [{ type: 'terminal', terminalId: owner.callId }],
    rawInput: { command: owner.command, cwd: owner.workingDirectory },
  }, undefined, { completion: MessageCompletion.UNSPECIFIED, supplementalContent: new Uint8Array() })
}

async function observeOnce(observe: () => Promise<boolean>): Promise<void> {
  expect(await observe()).toBe(true)
}

afterEach(() => vi.restoreAllMocks())

describe('junieOutputFilePathReady', () => {
  it('keeps the captured request-only row pending instead of invoking the strict decoder', () => {
    const reader = vi.spyOn(outputReaders, 'readJunieNativeOutputPaths')
    expect(junieOutputFilePathReady(pendingSnapshot(), owner)).toBe(false)
    expect(reader).not.toHaveBeenCalled()
  })

  it('keeps a partial native progress update pending without requiring its final fields', () => {
    const value = snapshot({ sessionUpdate: 'tool_call_update', toolCallId: owner.callId, status: 'in_progress' }, undefined, { completion: MessageCompletion.UNSPECIFIED, supplementalContent: new Uint8Array() })
    expect(junieOutputFilePathReady(value, owner)).toBe(false)
  })

  it('waits for the pointer property after the completed native row arrives', () => {
    expect(junieOutputFilePathReady(snapshot(completedFrame(), undefined, { supplementalContent: new Uint8Array() }), owner)).toBe(false)
    expect(junieOutputFilePathReady(snapshot(completedFrame(), { provider: { sessionUpdate: 'tool_call_update', toolCallId: owner.callId, status: 'completed' } }), owner)).toBe(false)
    expect(junieOutputFilePathReady(snapshot(), owner)).toBe(true)
  })

  it.each([null, false, 0, '', []])('settles a present malformed pointer for one strict decode: %j', (pointer) => {
    expect(junieOutputFilePathReady(snapshot(completedFrame(), { provider: providerReceipt(pointer) }), owner)).toBe(true)
  })

  it('refuses duplicate completed rows instead of selecting the latest row', () => {
    const value = snapshot()
    value.messages.push(create(AgentChatMessageSchema, { id: 'second-completed-row', seq: 2n, source: MessageSource.AGENT, agentSessionId: owner.sessionId, spanId: owner.callId, spanType: 'execute', content: encoder.encode(JSON.stringify(completedFrame())), contentCompression: ContentCompression.NONE }))
    expect(() => junieOutputFilePathReady(value, owner)).toThrow('duplicate completed rows')
  })

  it('settles an actual failed native command as failure instead of waiting for a pointer', () => {
    const value = snapshot({ ...completedFrame(), status: 'failed' })
    expect(() => junieOutputFilePathReady(value, owner)).toThrow('native command failed')
  })
})

describe('waitForJunieOutputFilePaths', () => {
  it('uses the fresh completed and enriched snapshot and decodes it exactly once', async () => {
    const reader = vi.spyOn(outputReaders, 'readJunieNativeOutputPaths')
    const ready = snapshot()
    const originalBytes = ready.messages[0]!.content.slice()
    const originalSupplement = ready.messages[0]!.supplementalContent.slice()
    const readSnapshot = vi.fn().mockResolvedValueOnce(pendingSnapshot()).mockResolvedValueOnce(snapshot(completedFrame(), { provider: { sessionUpdate: 'tool_call_update', toolCallId: owner.callId, status: 'completed' } })).mockResolvedValueOnce(ready)
    const result = await waitForJunieOutputFilePaths(owner, {
      readSnapshot,
      waitUntilSettled: async (observe) => {
        expect(await observe()).toBe(false)
        expect(reader).not.toHaveBeenCalled()
        expect(await observe()).toBe(false)
        expect(reader).not.toHaveBeenCalled()
        expect(await observe()).toBe(true)
        expect(await observe()).toBe(true)
      },
    })
    expect(readSnapshot).toHaveBeenCalledTimes(3)
    expect(reader).toHaveBeenCalledExactlyOnceWith(ready, owner.callId)
    expect(result.snapshot).toBe(ready)
    expect(result.receipt.paths).toEqual([path])
    expect(result.receipt.previewText).toBe('ORIGINAL_NATIVE_PREVIEW')
    expect(ready.messages[0]!.content).toEqual(originalBytes)
    expect(ready.messages[0]!.supplementalContent).toEqual(originalSupplement)
  })

  it.each([null, false, 0, '', []])('does not retry strict rejection of a present malformed pointer: %j', async (pointer) => {
    const reader = vi.spyOn(outputReaders, 'readJunieNativeOutputPaths')
    const readSnapshot = vi.fn().mockResolvedValueOnce(snapshot(completedFrame(), { provider: providerReceipt(pointer) })).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow()
    expect(readSnapshot).toHaveBeenCalledOnce()
    expect(reader).toHaveBeenCalledOnce()
  })

  it.each(['agentId', 'agentSessionId'] as const)('does not retry another snapshot %s', async (field) => {
    const value = snapshot()
    value[field] = 'foreign-owner'
    const readSnapshot = vi.fn().mockResolvedValueOnce(value).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, {
      readSnapshot,
      waitUntilSettled: async (observe) => {
        expect(await observe()).toBe(true)
        expect(await observe()).toBe(true)
      },
    })).rejects.toThrow('another agent or native session')
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it.each([
    { agentSessionId: 'foreign-session' },
    { spanType: 'foreign-tool' },
  ])('does not retry another row owner: %j', async (overrides) => {
    const readSnapshot = vi.fn().mockResolvedValue(snapshot(completedFrame(), { provider: providerReceipt() }, overrides))
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow('another native owner')
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it.each([
    { label: 'call', frame: { ...completedFrame(), toolCallId: 'foreign-call' } },
    { label: 'tool kind', frame: { ...completedFrame(), kind: 'read' } },
    { label: 'exit', frame: { ...completedFrame(), _meta: { terminal_exit: { terminal_id: owner.callId, exit_code: 7, signal: null } } } },
    { label: 'output', frame: { ...completedFrame(), rawOutput: { output: false } } },
  ])('does not retry malformed completed native $label fields', async ({ frame }) => {
    const readSnapshot = vi.fn().mockResolvedValueOnce(snapshot(frame)).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow()
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it.each([
    { command: 'foreign-command', cwd: owner.workingDirectory },
    { command: owner.command, cwd: '/foreign/project' },
  ])('refuses a self-consistent receipt for another expected command or cwd: %j', async (input) => {
    const frame = { ...completedFrame(), rawInput: input }
    const pointer = { ...pointerReceipt(), ...input }
    const readSnapshot = vi.fn().mockResolvedValue(snapshot(frame, { provider: providerReceipt(pointer) }))
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow('expected native command or working directory')
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it('preserves the original read failure without reading a later valid snapshot', async () => {
    const failure = new Error('The exact Worker read failed.')
    const readSnapshot = vi.fn().mockRejectedValueOnce(failure).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, {
      readSnapshot,
      waitUntilSettled: async (observe) => {
        expect(await observe()).toBe(true)
        expect(await observe()).toBe(true)
      },
    })).rejects.toBe(failure)
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it.each([undefined, null, false, 0, ''])('preserves a falsy read rejection without retry: %j', async (cause) => {
    const readSnapshot = vi.fn().mockRejectedValueOnce(cause).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toBe(cause)
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it.each([
    { content: encoder.encode('malformed native JSON') },
    { supplementalContent: encoder.encode('malformed native supplement JSON') },
  ])('does not retry a completed row with malformed native bytes: %j', async (overrides) => {
    const readSnapshot = vi.fn().mockResolvedValueOnce(snapshot(completedFrame(), { provider: providerReceipt() }, overrides)).mockResolvedValueOnce(snapshot())
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow('invalid JSON')
    expect(readSnapshot).toHaveBeenCalledOnce()
  })

  it('preserves the waiting operation failure', async () => {
    const failure = new Error('The observation deadline failed.')
    const readSnapshot = vi.fn()
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: async () => {
      throw failure
    } })).rejects.toBe(failure)
    expect(readSnapshot).not.toHaveBeenCalled()
  })

  it('refuses a wait that returns without a settled observation', async () => {
    const readSnapshot = vi.fn()
    await expect(waitForJunieOutputFilePaths(owner, { readSnapshot, waitUntilSettled: async () => {} })).rejects.toThrow('before its Worker row was ready')
    expect(readSnapshot).not.toHaveBeenCalled()
  })

  it('preserves an explicitly empty native preview after readiness', async () => {
    const ready = snapshot(completedFrame(''))
    const result = await waitForJunieOutputFilePaths(owner, { readSnapshot: async () => ready, waitUntilSettled: observeOnce })
    expect(result.receipt.previewText).toBe('')
    expect(result.receipt.paths).toEqual([path])
  })

  it('isolates simultaneous observations for different native calls', async () => {
    const secondOwner = { ...owner, callId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', command: 'node second-native-command.js' }
    const secondPath = path.replace('terminal-output-123.txt', 'terminal-output-456.txt')
    const secondFrame = {
      ...completedFrame('SECOND_NATIVE_PREVIEW'),
      toolCallId: secondOwner.callId,
      rawInput: { command: secondOwner.command, cwd: secondOwner.workingDirectory },
      _meta: { terminal_exit: { terminal_id: secondOwner.callId, exit_code: 0, signal: null } },
    }
    const secondPointer = { ...pointerReceipt(), toolCallId: secondOwner.callId, command: secondOwner.command, path: secondPath }
    const secondSnapshot = snapshot(secondFrame, { provider: { sessionUpdate: secondFrame.sessionUpdate, toolCallId: secondOwner.callId, status: secondFrame.status, outputFilePath: secondPointer } }, { id: 'second-call-result', spanId: secondOwner.callId })
    const [first, second] = await Promise.all([
      waitForJunieOutputFilePaths(owner, { readSnapshot: async () => snapshot(), waitUntilSettled: observeOnce }),
      waitForJunieOutputFilePaths(secondOwner, { readSnapshot: async () => secondSnapshot, waitUntilSettled: observeOnce }),
    ])
    expect(first.receipt.paths).toEqual([path])
    expect(first.receipt.previewText).toBe('ORIGINAL_NATIVE_PREVIEW')
    expect(second.receipt.paths).toEqual([secondPath])
    expect(second.receipt.previewText).toBe('SECOND_NATIVE_PREVIEW')
    expect(second.snapshot.messages[0]?.spanId).toBe(secondOwner.callId)
  })

  it('keeps the held operation until a ready row passes strict validation, then releases once', async () => {
    const entered = deferred<void>()
    const ready = deferred<NativeMessageSnapshot>()
    const release = vi.fn(async () => {})
    const proof = vi.fn()
    const result = withCleanup(async () => {
      const value = await waitForJunieOutputFilePaths(owner, {
        readSnapshot: () => ready.promise,
        waitUntilSettled: async (observe) => {
          entered.resolve()
          await observeOnce(observe)
        },
      })
      proof(value.receipt)
    }, release)
    await withCleanup(async () => {
      await entered.promise
      expect(release).not.toHaveBeenCalled()
      expect(proof).not.toHaveBeenCalled()
      ready.resolve(snapshot())
      await result
      expect(proof).toHaveBeenCalledOnce()
      expect(release).toHaveBeenCalledOnce()
      expect(proof.mock.invocationCallOrder[0]).toBeLessThan(release.mock.invocationCallOrder[0]!)
    }, async () => {
      ready.resolve(snapshot())
      await result
    })
  })

  it('retains strict proof and cleanup failures together', async () => {
    const cleanupFailure = new Error('The atomic release failed.')
    const operation = withCleanup(
      () => waitForJunieOutputFilePaths(owner, { readSnapshot: async () => snapshot(completedFrame(), { provider: providerReceipt(null) }), waitUntilSettled: observeOnce }),
      async () => {
        throw cleanupFailure
      },
    )
    await expect(operation).rejects.toMatchObject({ errors: [expect.any(Error), cleanupFailure] })
  })

  it.each(['agentId', 'sessionId', 'callId', 'command', 'workingDirectory'] as const)('refuses an empty expected %s before reading', async (field) => {
    const readSnapshot = vi.fn()
    await expect(waitForJunieOutputFilePaths({ ...owner, [field]: '' }, { readSnapshot, waitUntilSettled: observeOnce })).rejects.toThrow('exact native owner and command')
    expect(readSnapshot).not.toHaveBeenCalled()
  })
})
