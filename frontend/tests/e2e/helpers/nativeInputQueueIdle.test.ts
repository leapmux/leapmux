import type { ChannelManager } from '../../../src/lib/channel'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AgentInputKind, AgentInputState, WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { InnerStreamMessageSchema } from '../../../src/generated/proto/leapmux/v1/channel_pb'
import { WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { NativeInputQueueIdleCollector, waitForNativeInputQueueIdle } from './nativeInputQueueIdle'
import { WAIT_REPORT_MARGIN_MS } from './testDeadline'

const { getTestChannel } = vi.hoisted(() => ({ getTestChannel: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, getTestChannel }
})

afterEach(() => {
  getTestChannel.mockReset()
  vi.useRealTimers()
})

function acknowledgement(updateId = 1n, rejectedAgent?: string): Uint8Array {
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
    event: {
      case: 'updateAck',
      value: { updateId, rejectedAgents: rejectedAgent === undefined ? [] : [{ entityId: rejectedAgent }] },
    },
  }))
}

function queueFrame(agentId: string, options: { revision?: bigint, active?: boolean, paused?: boolean, innerId?: string, absent?: boolean } = {}): Uint8Array {
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
    event: {
      case: 'agentEvent',
      value: {
        agentId,
        event: {
          case: 'inputQueueChanged',
          value: options.absent
            ? {}
            : {
                snapshot: {
                  agentId: options.innerId ?? agentId,
                  revision: options.revision ?? 1n,
                  activeTurn: options.active ?? true,
                  paused: options.paused ?? false,
                },
              },
        },
      },
    },
  }))
}

describe('NativeInputQueueIdleCollector', () => {
  it('requires both the matching acknowledgement and a real idle snapshot', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(queueFrame('agent-1', { active: false, revision: 0n }))
    expect(collector.idleSnapshot).toBeUndefined()
    collector.accept(acknowledgement(2n, 'unrelated-agent'))
    expect(collector.subscribed).toBe(false)
    collector.accept(acknowledgement())
    expect(collector.idleSnapshot).toMatchObject({ agentId: 'agent-1', revision: 0n, activeTurn: false })
  })

  it('keeps an acknowledged active turn pending until its newer idle revision', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(acknowledgement())
    collector.accept(queueFrame('agent-1', { revision: 4n }))
    expect(collector.idleSnapshot).toBeUndefined()
    collector.accept(queueFrame('agent-1', { revision: 3n, active: false }))
    expect(collector.idleSnapshot).toBeUndefined()
    collector.accept(queueFrame('agent-1', { revision: 5n, active: false }))
    expect(collector.idleSnapshot?.revision).toBe(5n)
  })

  it.each(['canSteer', 'canPreempt'] as const)('accepts a native %s refresh without a queue revision change', (capability) => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    const initial = capability === 'canSteer'
    const frame = (enabled: boolean) => toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
      event: {
        case: 'agentEvent',
        value: {
          agentId: 'agent-1',
          event: {
            case: 'inputQueueChanged',
            value: {
              snapshot: {
                agentId: 'agent-1',
                revision: 7n,
                activeTurn: true,
                activeTurnSteerable: capability === 'canSteer',
                paused: capability === 'canSteer',
                items: [{
                  id: 'queued-native-input',
                  agentId: 'agent-1',
                  kind: capability === 'canSteer' ? AgentInputKind.USER_MESSAGE : AgentInputKind.PLAN_EXECUTION,
                  state: AgentInputState.QUEUED,
                  [capability]: enabled,
                }],
              },
            },
          },
        },
      },
    }))
    collector.accept(acknowledgement())
    collector.accept(frame(initial))
    expect(() => collector.accept(frame(!initial))).not.toThrow()
    expect(collector.idleSnapshot).toBeUndefined()
    collector.accept(queueFrame('agent-1', { revision: 8n, active: false }))
    expect(collector.idleSnapshot).toMatchObject({ agentId: 'agent-1', revision: 8n, activeTurn: false })
  })

  it('accepts a repeated identical revision without changing its native state', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(acknowledgement())
    const frame = queueFrame('agent-1', { active: false })
    collector.accept(frame)
    const original = collector.idleSnapshot
    collector.accept(frame)
    expect(collector.idleSnapshot).toEqual(original)
  })

  it.each([{ active: false }])('rejects conflicting native state at one revision: %j', (change) => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(queueFrame('agent-1'))
    expect(() => collector.accept(queueFrame('agent-1', change))).toThrow('changed at the same revision')
  })

  it('preserves the maximum uint64 revision', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(acknowledgement())
    const revision = (1n << 64n) - 1n
    collector.accept(queueFrame('agent-1', { revision, active: false }))
    expect(collector.idleSnapshot?.revision).toBe(revision)
  })

  it('ignores a foreign outer agent before it reads an absent snapshot', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(acknowledgement())
    collector.accept(queueFrame('another-agent', { absent: true }))
    expect(collector.idleSnapshot).toBeUndefined()
  })

  it('ignores unrelated native agent events', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    collector.accept(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
      event: { case: 'agentEvent', value: { agentId: 'agent-1', event: { case: 'catchUpComplete', value: {} } } },
    })))
    expect(collector.idleSnapshot).toBeUndefined()
  })

  it('rejects an absent queue snapshot', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    expect(() => collector.accept(queueFrame('agent-1', { absent: true }))).toThrow('no queue snapshot')
  })

  it.each(['', 'different-agent'])('rejects an inner snapshot with a different agent ID: %j', (innerId) => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    expect(() => collector.accept(queueFrame('agent-1', { innerId }))).toThrow('different agent')
  })

  it('rejects a refused subscription', () => {
    const collector = new NativeInputQueueIdleCollector('agent-1')
    expect(() => collector.accept(acknowledgement(1n, 'agent-1'))).toThrow('refused')
    expect(collector.subscribed).toBe(false)
  })

  it('rejects malformed protobuf data', () => {
    expect(() => new NativeInputQueueIdleCollector('agent-1').accept(Uint8Array.of(0xFF))).toThrow()
  })

  it.each(['', ' \n\t'])('rejects an empty agent ID: %j', (agentId) => {
    expect(() => new NativeInputQueueIdleCollector(agentId)).toThrow('requires an agent ID')
  })
})

type QueueWatch = ReturnType<ChannelManager['stream']>

function nativeStream() {
  let onMessage: Parameters<QueueWatch['onMessage']>[0] | undefined
  let onError: Parameters<QueueWatch['onError']>[0] | undefined
  let onEnd: Parameters<QueueWatch['onEnd']>[0] | undefined
  let signalReady: (() => void) | undefined
  const ready = new Promise<void>((resolve) => {
    signalReady = resolve
  })
  const cancel = vi.fn()
  const watch = {
    requestId: 1,
    onMessage: (listener: Parameters<QueueWatch['onMessage']>[0]) => {
      onMessage = listener
    },
    onError: (listener: Parameters<QueueWatch['onError']>[0]) => {
      onError = listener
    },
    onEnd: (listener: Parameters<QueueWatch['onEnd']>[0]) => {
      onEnd = listener
      signalReady?.()
    },
    cancel,
    send: vi.fn(),
  } satisfies QueueWatch
  const channel = {
    getOrOpenChannel: vi.fn(async () => 'native-channel'),
    stream: vi.fn<ChannelManager['stream']>(() => watch),
  }
  return {
    channel,
    watch,
    ready,
    cancel,
    message: (payload: Uint8Array) => {
      if (!onMessage)
        throw new Error('The test stream has no message handler.')
      onMessage(create(InnerStreamMessageSchema, { payload }))
    },
    error: (error: Error) => {
      if (!onError)
        throw new Error('The test stream has no error handler.')
      onError(error)
    },
    end: () => {
      if (!onEnd)
        throw new Error('The test stream has no end handler.')
      onEnd()
    },
  }
}

const server = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'worker-1' }
const testDeadline = () => Date.now() + WAIT_REPORT_MARGIN_MS + 60_000

describe('waitForNativeInputQueueIdle', () => {
  it('sends the exact native subscription and cancels after acknowledged completion', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    expect(getTestChannel).toHaveBeenCalledExactlyOnceWith(server.hubUrl, server.adminToken)
    expect(stream.channel.getOrOpenChannel).toHaveBeenCalledExactlyOnceWith(server.workerId)
    const request = stream.channel.stream.mock.calls[0]
    expect(request).toBeDefined()
    if (!request)
      throw new Error('The input queue wait did not create its native stream.')
    const [channelId, method, payload] = request
    expect(channelId).toBe('native-channel')
    expect(method).toBe('WatchEvents')
    expect(fromBinary(WatchEventsRequestSchema, payload)).toMatchObject({
      agents: [{ agentId: 'agent-1', mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n }],
      updateId: 1n,
    })
    stream.message(queueFrame('agent-1', { active: false }))
    expect(stream.cancel).not.toHaveBeenCalled()
    stream.message(acknowledgement())
    await expect(waiting).resolves.toMatchObject({ agentId: 'agent-1', activeTurn: false })
    expect(stream.cancel).toHaveBeenCalledExactlyOnceWith()
  })

  it('waits for real native active-to-idle output without a completion timer', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    stream.message(acknowledgement())
    stream.message(queueFrame('agent-1', { revision: 8n }))
    expect(stream.cancel).not.toHaveBeenCalled()
    stream.message(queueFrame('agent-1', { revision: 9n, active: false }))
    await expect(waiting).resolves.toMatchObject({ revision: 9n })
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it.each([
    { frame: queueFrame('agent-1', { absent: true }), message: 'no queue snapshot' },
    { frame: queueFrame('agent-1', { innerId: 'other-agent' }), message: 'different agent' },
    { frame: acknowledgement(1n, 'agent-1'), message: 'refused' },
  ])('cancels when the native stream fails validation: $message', async ({ frame, message }) => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    stream.message(frame)
    await expect(waiting).rejects.toThrow(message)
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('preserves a native transport failure and cancels its stream', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    const failure = new Error('The native transport disconnected.')
    stream.error(failure)
    await expect(waiting).rejects.toBe(failure)
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('rejects a stream that ends before acknowledged completion', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    stream.end()
    await expect(waiting).rejects.toThrow('ended before completion')
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('keeps both the native failure and its cancellation failure', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    const nativeFailure = new Error('The native stream failed.')
    const cleanupFailure = new Error('The stream cancellation failed.')
    stream.cancel.mockImplementation(() => {
      throw cleanupFailure
    })
    stream.error(nativeFailure)
    await expect(waiting).rejects.toMatchObject({ errors: [nativeFailure, cleanupFailure] })
  })

  it('reports a cancellation failure after actual completion', async () => {
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    await stream.ready
    const failure = new Error('The stream cancellation failed.')
    stream.cancel.mockImplementation(() => {
      throw failure
    })
    stream.message(acknowledgement())
    stream.message(queueFrame('agent-1', { active: false }))
    await expect(waiting).rejects.toBe(failure)
  })

  it('preserves a connection failure before a stream exists', async () => {
    const failure = new Error('The encrypted channel failed.')
    getTestChannel.mockRejectedValue(failure)
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)).rejects.toBe(failure)
  })

  it('preserves a Worker channel failure before a stream exists', async () => {
    const stream = nativeStream()
    const failure = new Error('The Worker channel failed.')
    stream.channel.getOrOpenChannel.mockRejectedValue(failure)
    getTestChannel.mockResolvedValue(stream.channel)
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)).rejects.toBe(failure)
    expect(stream.channel.stream).not.toHaveBeenCalled()
  })

  it('preserves a failure to create the native stream', async () => {
    const stream = nativeStream()
    const failure = new Error('The native stream could not start.')
    stream.channel.stream.mockImplementation(() => {
      throw failure
    })
    getTestChannel.mockResolvedValue(stream.channel)
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)).rejects.toBe(failure)
    expect(stream.cancel).not.toHaveBeenCalled()
  })

  it('cancels a stream after its handler registration fails', async () => {
    const stream = nativeStream()
    const failure = new Error('The native handler could not start.')
    stream.channel.stream.mockReturnValue({
      ...stream.watch,
      onMessage: () => {
        throw failure
      },
    })
    getTestChannel.mockResolvedValue(stream.channel)
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)).rejects.toBe(failure)
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('rejects an empty agent ID before reading the deadline or opening a channel', async () => {
    const deadline = vi.fn(testDeadline)
    await expect(waitForNativeInputQueueIdle(server, '', deadline)).rejects.toThrow('requires an agent ID')
    expect(deadline).not.toHaveBeenCalled()
    expect(getTestChannel).not.toHaveBeenCalled()
  })

  it('preserves a failure to read the current whole-test deadline', async () => {
    const failure = new Error('The test fixture could not read its deadline.')
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', () => {
      throw failure
    })).rejects.toBe(failure)
    expect(getTestChannel).not.toHaveBeenCalled()
  })

  it.each([undefined, Number.NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])('rejects an absent or invalid whole-test deadline: %s', async (deadline) => {
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', () => deadline)).rejects.toThrow('requires the current whole-test deadline')
    expect(getTestChannel).not.toHaveBeenCalled()
  })

  it.each([-1, 0, Date.now()])('rejects an exhausted whole-test deadline before opening a channel: %s', async (deadline) => {
    await expect(waitForNativeInputQueueIdle(server, 'agent-1', () => deadline)).rejects.toThrow('leaves no time')
    expect(getTestChannel).not.toHaveBeenCalled()
  })

  it('cancels a pending stream at the existing whole-test failure deadline', async () => {
    vi.useFakeTimers()
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const deadline = Date.now() + WAIT_REPORT_MARGIN_MS + 100
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', () => deadline)
    const rejected = expect(waiting).rejects.toThrow('did not complete before the whole-test deadline')
    await stream.ready
    await vi.advanceTimersByTimeAsync(100)
    await rejected
    expect(stream.cancel).toHaveBeenCalledOnce()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not create a late stream after the deadline ends during channel setup', async () => {
    vi.useFakeTimers()
    const stream = nativeStream()
    let release: ((value: string) => void) | undefined
    let signalEntry: (() => void) | undefined
    const entered = new Promise<void>((resolve) => {
      signalEntry = resolve
    })
    const opened = new Promise<string>((resolve) => {
      release = resolve
    })
    stream.channel.getOrOpenChannel.mockImplementation(() => {
      signalEntry?.()
      return opened
    })
    getTestChannel.mockResolvedValue(stream.channel)
    const deadline = Date.now() + WAIT_REPORT_MARGIN_MS + 100
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', () => deadline)
    const rejected = expect(waiting).rejects.toThrow('whole-test deadline')
    await entered
    await vi.advanceTimersByTimeAsync(100)
    await rejected
    release?.('late-channel')
    await opened
    expect(stream.channel.stream).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('handles a very large whole-test deadline without timer overflow', async () => {
    vi.useFakeTimers()
    const stream = nativeStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const waiting = waitForNativeInputQueueIdle(server, 'agent-1', () => Number.MAX_SAFE_INTEGER)
    await stream.ready
    await vi.advanceTimersByTimeAsync(2_147_483_647)
    expect(stream.cancel).not.toHaveBeenCalled()
    stream.message(acknowledgement())
    stream.message(queueFrame('agent-1', { active: false }))
    await expect(waiting).resolves.toMatchObject({ agentId: 'agent-1' })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps concurrent Worker agent subscriptions separate', async () => {
    const first = nativeStream()
    const second = nativeStream()
    getTestChannel.mockResolvedValueOnce(first.channel).mockResolvedValueOnce(second.channel)
    const firstWait = waitForNativeInputQueueIdle(server, 'agent-1', testDeadline)
    const secondWait = waitForNativeInputQueueIdle(server, 'agent-2', testDeadline)
    await Promise.all([first.ready, second.ready])
    for (const stream of [first, second])
      stream.message(acknowledgement())
    first.message(queueFrame('agent-2', { active: false }))
    second.message(queueFrame('agent-1', { active: false }))
    expect(first.cancel).not.toHaveBeenCalled()
    expect(second.cancel).not.toHaveBeenCalled()
    first.message(queueFrame('agent-1', { active: false }))
    await expect(firstWait).resolves.toMatchObject({ agentId: 'agent-1' })
    expect(second.cancel).not.toHaveBeenCalled()
    second.message(queueFrame('agent-2', { active: false }))
    await expect(secondWait).resolves.toMatchObject({ agentId: 'agent-2' })
    expect(first.cancel).toHaveBeenCalledOnce()
    expect(second.cancel).toHaveBeenCalledOnce()
  })
})
