import type { ChannelManager } from '~/lib/channel'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema, WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '~/generated/proto/leapmux/v1/workspace_pb'
import { fakeChannelStream } from '~/test-support/channelStreamFake'

const fixture = vi.hoisted(() => ({ current: undefined as ReturnType<typeof fakeChannelStream> | undefined }))

vi.mock('~/lib/channel', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/channel')>()
  return {
    ...actual,
    ChannelManager: class {
      getOrOpenChannel(...args: Parameters<ChannelManager['getOrOpenChannel']>) {
        if (!fixture.current)
          throw new Error('The test requires its channel fixture.')
        const delegate: Pick<ChannelManager, 'getOrOpenChannel' | 'stream'> = fixture.current.channel
        return delegate.getOrOpenChannel(...args)
      }

      stream(...args: Parameters<ChannelManager['stream']>) {
        if (!fixture.current)
          throw new Error('The test requires its stream fixture.')
        return fixture.current.channel.stream(...args)
      }
    },
  }
})

vi.mock('~/api/transport', () => ({ transport: {}, apiLoadingTimeoutMs: () => 30_000 }))

const { watchEventsViaChannel } = await import('./workerRpc')

beforeEach(() => {
  fixture.current = fakeChannelStream()
})

describe('watchEventsViaChannel', () => {
  it.each([-(1n << 63n), 0n, (1n << 63n) - 1n])('preserves maximum replay identity and signed cursor %s in actual opening bytes', async (cursorSeq) => {
    const stream = fixture.current!
    const replayId = (1n << 64n) - 1n
    const handle = await watchEventsViaChannel('worker-1', {
      updateId: 17n,
      agents: [{ agentId: 'agent-1', mode: WatchMode.FULL, replay: WatchReplayMode.AFTER_CURSOR, replayId, cursorSeq, windowTailSeq: 0n }],
    })
    const [channelId, method, payload] = stream.channel.stream.mock.calls[0] ?? []
    if (!(payload instanceof Uint8Array))
      throw new Error('The opening call must transmit protobuf bytes.')
    expect(channelId).toBe('native-channel')
    expect(method).toBe('WatchEvents')
    const encoded = fromBinary(WatchEventsRequestSchema, payload)
    expect(encoded.updateId).toBe(17n)
    expect(encoded.agents[0]).toMatchObject({ replayId, cursorSeq, windowTailSeq: 0n })
    handle.close()
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('preserves a carried replay identity and cursor while the actual update ID changes', async () => {
    const stream = fixture.current!
    const handle = await watchEventsViaChannel('worker-1', {
      updateId: 11n,
      agents: [{ agentId: 'agent-1', mode: WatchMode.FULL, replayId: 11n, cursorSeq: 7n, windowTailSeq: 6n }],
    })
    handle.update({
      updateId: 12n,
      agents: [
        { agentId: 'agent-1', mode: WatchMode.FULL, replayId: 11n, cursorSeq: 7n, windowTailSeq: 6n },
        { agentId: 'agent-2', mode: WatchMode.NOTIFY, replayId: 0n },
      ],
    })
    const payload = stream.watch.send.mock.calls[0]?.[0]
    if (!(payload instanceof Uint8Array))
      throw new Error('The update call must transmit protobuf bytes.')
    const encoded = fromBinary(WatchEventsRequestSchema, payload)
    expect(encoded.updateId).toBe(12n)
    expect(encoded.agents[0]).toMatchObject({ replayId: 11n, cursorSeq: 7n, windowTailSeq: 6n })
    expect(encoded.agents[1]).toMatchObject({ agentId: 'agent-2', mode: WatchMode.NOTIFY, replayId: 0n })
    expect(stream.channel.stream).toHaveBeenCalledOnce()
    handle.close()
  })

  it('decodes the replay identity and actual registration state from received bytes', async () => {
    const stream = fixture.current!
    const handle = await watchEventsViaChannel('worker-1', {
      updateId: 12n,
      agents: [{ agentId: 'agent-1', mode: WatchMode.FULL, replayId: 11n }],
    })
    const received = vi.fn()
    handle.onEvent(received)
    stream.message(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: {
      updateId: 12n,
      agentStates: [{ agentId: 'agent-1', mode: WatchMode.FULL, replayId: 11n }],
    } } })))
    stream.message(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: create(AgentEventSchema, {
      agentId: 'agent-1',
      replay: true,
      replayId: 11n,
      event: { case: 'catchUpStart', value: { latestSeq: 7n } },
      replayAgentId: 'agent-1',
    }) } })))
    expect(received.mock.calls[0]?.[0].event.value.agentStates).toEqual([expect.objectContaining({ agentId: 'agent-1', mode: WatchMode.FULL, replayId: 11n })])
    expect(received.mock.calls[1]?.[0].event.value.replayId).toBe(11n)
    expect(Reflect.get(received.mock.calls[1]?.[0].event.value, 'replayAgentId')).toBe('agent-1')
    handle.close()
  })

  it.each([9007199254740993n, (1n << 64n) - 1n])('decodes the exact root destination and child origin for replay %s', async (replayId) => {
    const stream = fixture.current!
    const handle = await watchEventsViaChannel('worker-1', { updateId: replayId, agents: [{ agentId: 'child-1', mode: WatchMode.FULL, replayId }] })
    const received = vi.fn()
    handle.onEvent(received)
    const event = create(AgentEventSchema, {
      agentId: 'root-1',
      replay: true,
      replayId,
      event: { case: 'backgroundTasksChanged', value: { agentId: 'root-1', tasks: [] } },
      replayAgentId: 'child-1',
    })
    stream.message(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: event } })))
    expect(received).toHaveBeenCalledOnce()
    expect(received.mock.calls[0]?.[0].event.value).toMatchObject({ agentId: 'root-1', replay: true, replayId, replayAgentId: 'child-1' })
    handle.close()
  })
})
