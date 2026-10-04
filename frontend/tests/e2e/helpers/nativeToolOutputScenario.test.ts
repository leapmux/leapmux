import type { MockModelRequestRecord } from './mockModelScript'
import type { NativeMessageSnapshot } from './nativeMessages'
import { runInNewContext } from 'node:vm'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it, vi } from 'vitest'
import { AgentInfoSchema, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { computedNativeToolOutput } from './nativeToolOutput'
import { nativeOutputFileCommand, runNativeToolOutputCapture } from './nativeToolOutputScenario'

const context = { provider: AgentProvider.PI }
const call = { id: 'call_native', name: 'native_tool', arguments: { source: 'computed source' } }
const output = { source: 'compute(70 + 7)', text: 'FIRST42\nMIDDLE77\nLAST42', firstMarker: 'FIRST42', omittedMarker: 'MIDDLE77', lastMarker: 'LAST42' }

describe('nativeOutputFileCommand', () => {
  it('keeps the physical command on one line for native command validators', () => {
    const expected = computedNativeToolOutput({ prefix: 'CommandProbe', lineCount: 5 })
    expect(nativeOutputFileCommand(expected)).not.toContain('\n')
  })

  it('executes the computed source and writes every output byte through the real statement separator', () => {
    const expected = computedNativeToolOutput({ prefix: 'CommandProbe', lineCount: 5 })
    const command = nativeOutputFileCommand(expected)
    expect(command).toMatch(/^node -e '[\s\S]*'$/u)
    const program = command.slice('node -e \''.length, -1)
    let actual = ''
    runInNewContext(program, {
      process: {
        stdout: {
          write: (chunk: string) => {
            actual += chunk
          },
        },
      },
    }, { timeout: 1000 })
    expect(actual).toBe(expected.text)
  })
})

function fixture() {
  const events: string[] = []
  const agent = create(AgentInfoSchema, { id: 'agent', agentSessionId: 'session' })
  const snapshot = { agentId: agent.id, agentSessionId: agent.agentSessionId, messages: [] }
  const request = { protocol: 'openai-chat-completions' as const, path: '/v1/chat/completions', stepIndex: 5, body: {}, mockCredential: { kind: 'bearer' as const, accepted: true } }
  const operations = {
    prepare: async () => { events.push('prepare') },
    queue: async () => {
      events.push('queue')
      return 4
    },
    send: async () => { events.push('send') },
    wait: async (target: number, beforeIdle: () => Promise<void>) => {
      expect(target).toBe(6)
      events.push('model-complete')
      await beforeIdle()
      events.push('idle')
    },
    request: async (step: number) => {
      expect(step).toBe(5)
      events.push('request')
      return request
    },
    agent: async () => {
      events.push('agent')
      return agent
    },
    snapshot: async () => {
      events.push('snapshot')
      return snapshot
    },
    attach: async () => { events.push('attach') },
    proof: async () => { events.push('proof') },
  }
  return { operations, events, agent, snapshot, request }
}

describe('runNativeToolOutputCapture', () => {
  it('attaches native evidence before idle and rereads complete rows before provider proof', async () => {
    const f = fixture()
    const capture = await runNativeToolOutputCapture(context, call, output, f.operations)
    expect(f.events).toEqual(['prepare', 'agent', 'queue', 'send', 'model-complete', 'request', 'agent', 'snapshot', 'attach', 'idle', 'request', 'agent', 'snapshot', 'attach', 'proof'])
    expect(capture.nativeCallId).toBe(call.id)
    expect(capture.request).toBe(f.request)
    expect(capture.output).toBe(output)
  })

  it('keeps the early native attachment when the idle condition fails', async () => {
    const f = fixture()
    const failure = new Error('The actual native turn did not reach idle.')
    f.operations.wait = async (_target, beforeIdle) => {
      await beforeIdle()
      throw failure
    }
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toBe(failure)
    expect(f.events).toContain('attach')
    expect(f.events).not.toContain('proof')
  })

  it('runs the early native packet proof once after attachment and before idle', async () => {
    const f = fixture()
    const earlyProof = vi.fn(async () => {
      f.events.push('early-proof')
    })
    await runNativeToolOutputCapture(context, call, output, { ...f.operations, earlyProof })
    expect(earlyProof).toHaveBeenCalledTimes(1)
    expect(f.events.indexOf('attach')).toBeLessThan(f.events.indexOf('early-proof'))
    expect(f.events.indexOf('early-proof')).toBeLessThan(f.events.indexOf('idle'))
  })

  it('keeps exact native evidence when the early native packet proof fails', async () => {
    const f = fixture()
    const failure = new Error('The native result packet has a different owner.')
    await expect(runNativeToolOutputCapture(context, call, output, {
      ...f.operations,
      earlyProof: async () => {
        throw failure
      },
    })).rejects.toBe(failure)
    expect(f.events).toContain('attach')
    expect(f.events).not.toContain('idle')
    expect(f.events).not.toContain('proof')
  })

  it('propagates provider proof failure after the exact native records attach', async () => {
    const f = fixture()
    const failure = new Error('The provider supplied no native output pointer.')
    f.operations.proof = async () => {
      f.events.push('proof')
      throw failure
    }
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toBe(failure)
    expect(f.events.at(-2)).toBe('attach')
    expect(f.events.at(-1)).toBe('proof')
  })

  it('retains the provider resolved call ID independently from the scripted ID', async () => {
    const f = fixture()
    const nativeCallId = vi.fn((_request: MockModelRequestRecord, scriptedId: string) => `actual_${scriptedId}`)
    const capture = await runNativeToolOutputCapture(context, call, output, { ...f.operations, nativeCallId })
    expect(capture.call.id).toBe('call_native')
    expect(capture.nativeCallId).toBe('actual_call_native')
    expect(nativeCallId).toHaveBeenCalledWith(f.request, call.id, f.snapshot)
  })

  it('supplies the owned Worker snapshot to a provider that resolves its actual native call ID', async () => {
    const f = fixture()
    const nativeCallId = vi.fn((_request: MockModelRequestRecord, scriptedId: string, snapshot?: NativeMessageSnapshot) => {
      return snapshot ? `${scriptedId}_${snapshot.agentSessionId}` : 'missing-worker-snapshot'
    })
    const capture = await runNativeToolOutputCapture(context, call, output, { ...f.operations, nativeCallId })
    expect(capture.nativeCallId).toBe('call_native_session')
    expect(nativeCallId).toHaveBeenCalledTimes(2)
    expect(nativeCallId).toHaveBeenNthCalledWith(1, f.request, call.id, f.snapshot)
    expect(nativeCallId).toHaveBeenNthCalledWith(2, f.request, call.id, f.snapshot)
  })

  it('attaches a refused mock credential receipt before it rejects provider proof', async () => {
    const f = fixture()
    f.request.mockCredential.accepted = false
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('isolated mock credential')
    expect(f.events).toContain('attach')
    expect(f.events).not.toContain('proof')
  })

  it('rejects a changed native session before any provider proof', async () => {
    const f = fixture()
    f.snapshot.agentSessionId = 'foreign'
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('native session identity')
    expect(f.events).not.toContain('proof')
  })

  it('rejects a consistent different session after the early evidence capture', async () => {
    const f = fixture()
    f.operations.wait = async (_target, beforeIdle) => {
      await beforeIdle()
      f.agent.agentSessionId = 'new-session'
      f.snapshot.agentSessionId = 'new-session'
    }
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('changed its native session')
    expect(f.events).not.toContain('proof')
  })

  it('binds a native session that the first real model turn creates', async () => {
    const f = fixture()
    f.agent.agentSessionId = ''
    f.operations.wait = async (_target, beforeIdle) => {
      f.agent.agentSessionId = 'created-by-first-turn'
      f.snapshot.agentSessionId = 'created-by-first-turn'
      await beforeIdle()
    }
    const capture = await runNativeToolOutputCapture(context, call, output, f.operations)
    expect(capture.agent.agentSessionId).toBe('created-by-first-turn')
    expect(f.events).toContain('proof')
  })

  it.each([{ ...output, source: '' }, { ...output, text: '' }, { ...output, omittedMarker: '' }, { ...output, source: output.text }, { ...output, source: output.omittedMarker }])('rejects an invalid computed output before native invocation %j', async (invalid) => {
    const f = fixture()
    await expect(runNativeToolOutputCapture(context, call, invalid, f.operations)).rejects.toThrow('limited computed output')
    expect(f.events).toEqual([])
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER])('rejects an invalid queue index %j before sending the prompt', async (index) => {
    const f = fixture()
    f.operations.queue = async () => index
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('invalid queue index')
    expect(f.events).not.toContain('send')
    expect(f.events).not.toContain('proof')
  })

  it('refuses a different model step before attaching unrelated records', async () => {
    const f = fixture()
    f.request.stepIndex = 6
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('different model step')
    expect(f.events).not.toContain('attach')
    expect(f.events).not.toContain('proof')
  })

  it('refuses a different working directory before attaching unrelated records', async () => {
    const f = fixture()
    f.operations.wait = async (_target, beforeIdle) => {
      f.agent.workingDir = '/different/native/project'
      await beforeIdle()
    }
    await expect(runNativeToolOutputCapture(context, call, output, f.operations)).rejects.toThrow('working directory')
    expect(f.events).not.toContain('attach')
  })

  it('refuses an empty resolved call ID before provider proof', async () => {
    const f = fixture()
    await expect(runNativeToolOutputCapture(context, call, output, {
      ...f.operations,
      nativeCallId: () => '',
    })).rejects.toThrow('no actual call ID')
    expect(f.events).not.toContain('attach')
    expect(f.events).not.toContain('proof')
  })

  it.each([
    { ...output, source: 'x'.repeat(512 * 1024 + 1) },
    { ...output, text: `${output.text}${'x'.repeat(4 * 1024 * 1024)}` },
  ])('rejects generated data that exceeds capture limits', async (invalid) => {
    const f = fixture()
    await expect(runNativeToolOutputCapture(context, call, invalid, f.operations)).rejects.toThrow('limited computed output')
    expect(f.events).toEqual([])
  })
})
