import type { AgentEvent, WatchAgentState } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeChannelStream } from '~/test-support/channelStreamFake'
import { WatchReplayMode } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema, WatchAgentStateSchema, WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { AGENT_WATCH_UPDATE_ID, AgentEventCollector, agentWatchRequest, readAgentWatchFrame, watchAgentEvents } from './agentEventWatch'

const { getTestChannel } = vi.hoisted(() => ({ getTestChannel: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, getTestChannel }
})

afterEach(() => {
  getTestChannel.mockReset()
})

const server = { hubUrl: 'http://mock.invalid', adminToken: 'mock-admin', workerId: 'worker-1' }

function acknowledgement(updateId = AGENT_WATCH_UPDATE_ID, rejectedAgent?: string, states: WatchAgentState[] = [create(WatchAgentStateSchema, {
  agentId: 'agent-1',
  mode: WatchMode.FULL,
  replayId: AGENT_WATCH_UPDATE_ID,
})]): Uint8Array {
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
    event: { case: 'updateAck', value: { updateId, rejectedAgents: rejectedAgent === undefined ? [] : [{ entityId: rejectedAgent }], agentStates: states } },
  }))
}

/** A frame that carries one input-queue event, whose revision a test selects. */
function queueEvent(agentId: string, revision: bigint): Uint8Array {
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
    event: { case: 'agentEvent', value: { agentId, event: { case: 'inputQueueChanged', value: { snapshot: { agentId, revision } } } } },
  }))
}

function replayQueueEvent(origin: string, replayId = AGENT_WATCH_UPDATE_ID, destination = 'agent-1'): Uint8Array {
  const event = create(AgentEventSchema, {
    agentId: destination,
    replay: true,
    replayId,
    event: { case: 'inputQueueChanged', value: { snapshot: { agentId: destination, revision: 7n } } },
    replayAgentId: origin,
  })
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: event } }))
}

/** Keep the revision of each input-queue event, and skip every other event. */
function queueRevision(event: AgentEvent): bigint | undefined {
  return event.event.case === 'inputQueueChanged' ? event.event.value.snapshot?.revision : undefined
}

describe('agentWatchRequest', () => {
  it('subscribes to the live events of one agent with the shared update ID', () => {
    expect(fromBinary(WatchEventsRequestSchema, agentWatchRequest('agent-1'))).toMatchObject({
      agents: [{ agentId: 'agent-1', mode: WatchMode.FULL, replay: WatchReplayMode.LATEST, cursorSeq: 0n, replayId: AGENT_WATCH_UPDATE_ID }],
      updateId: AGENT_WATCH_UPDATE_ID,
    })
  })

  it.each(['', ' \t'])('refuses an empty agent ID: %j', (agentId) => {
    expect(() => agentWatchRequest(agentId)).toThrow('requires an agent ID')
  })
})

describe('readAgentWatchFrame', () => {
  const fullState = () => create(WatchAgentStateSchema, { agentId: 'agent-1', mode: WatchMode.FULL, replayId: AGENT_WATCH_UPDATE_ID })

  it.each([
    { label: 'no registered state', states: () => [] },
    { label: 'another agent', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-2', mode: WatchMode.FULL, replayId: AGENT_WATCH_UPDATE_ID })] },
    { label: 'an empty agent ID', states: () => [create(WatchAgentStateSchema, { mode: WatchMode.FULL, replayId: AGENT_WATCH_UPDATE_ID })] },
    { label: 'an unspecified mode', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-1', replayId: AGENT_WATCH_UPDATE_ID })] },
    { label: 'NOTIFY mode', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-1', mode: WatchMode.NOTIFY })] },
    { label: 'zero replay identity', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-1', mode: WatchMode.FULL })] },
    { label: 'a different replay identity', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-1', mode: WatchMode.FULL, replayId: 2n })] },
    { label: 'a large replay identity', states: () => [create(WatchAgentStateSchema, { agentId: 'agent-1', mode: WatchMode.FULL, replayId: 18446744073709551615n })] },
    { label: 'a repeated matching state', states: () => [fullState(), fullState()] },
  ])('refuses $label in the actual registration acknowledgement', ({ states }) => {
    expect(() => readAgentWatchFrame(acknowledgement(AGENT_WATCH_UPDATE_ID, undefined, states()), 'agent-1', 'test watch'))
      .toThrow('The Worker did not register the test watch with its requested FULL replay identity.')
  })

  it('accepts the exact registered identity beside another registered agent', () => {
    const states = [create(WatchAgentStateSchema, { agentId: 'agent-2', mode: WatchMode.NOTIFY }), fullState()]
    expect(readAgentWatchFrame(acknowledgement(AGENT_WATCH_UPDATE_ID, undefined, states), 'agent-1', 'test watch')).toEqual({ kind: 'acknowledged' })
  })

  it('reads the acknowledgement of the shared update, and ignores one of another update even when it refuses', () => {
    expect(readAgentWatchFrame(acknowledgement(), 'agent-1', 'test watch')).toEqual({ kind: 'acknowledged' })
    expect(readAgentWatchFrame(acknowledgement(2n, 'agent-1'), 'agent-1', 'test watch')).toEqual({ kind: 'ignored' })
  })

  it('refuses a rejected subscription with the watch label and the rejected agents', () => {
    expect(() => readAgentWatchFrame(acknowledgement(AGENT_WATCH_UPDATE_ID, 'agent-1'), 'agent-1', 'test watch'))
      .toThrow(/^The Worker refused the test watch: .*agent-1/)
  })

  it('returns an event of the watched agent and ignores an event of another agent', () => {
    const frame = readAgentWatchFrame(queueEvent('agent-1', 3n), 'agent-1', 'test watch')
    expect(frame.kind).toBe('event')
    expect(frame.kind === 'event' ? queueRevision(frame.event) : undefined).toBe(3n)
    expect(readAgentWatchFrame(queueEvent('agent-2', 3n), 'agent-1', 'test watch')).toEqual({ kind: 'ignored' })
  })

  it.each([
    { label: 'the exact origin and lifetime', origin: 'agent-1', replayId: AGENT_WATCH_UPDATE_ID, accepted: true },
    { label: 'an empty origin', origin: '', replayId: AGENT_WATCH_UPDATE_ID, accepted: false },
    { label: 'a foreign origin', origin: 'agent-2', replayId: AGENT_WATCH_UPDATE_ID, accepted: false },
    { label: 'a zero lifetime', origin: 'agent-1', replayId: 0n, accepted: false },
    { label: 'another lifetime', origin: 'agent-1', replayId: 2n, accepted: false },
    { label: 'the maximum lifetime', origin: 'agent-1', replayId: (1n << 64n) - 1n, accepted: false },
  ])('selects replay only with its requested identity: $label', ({ origin, replayId, accepted }) => {
    const frame = readAgentWatchFrame(replayQueueEvent(origin, replayId), 'agent-1', 'test watch')
    expect(frame.kind).toBe(accepted ? 'event' : 'ignored')
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.accept(acknowledgement())
    collector.accept(replayQueueEvent(origin, replayId))
    collector.assertHealthy()
    expect(collector.items).toEqual(accepted ? [7n] : [])
  })

  it('ignores another replay destination even when its origin matches the watch', () => {
    expect(readAgentWatchFrame(replayQueueEvent('agent-1', AGENT_WATCH_UPDATE_ID, 'agent-2'), 'agent-1', 'test watch')).toEqual({ kind: 'ignored' })
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.accept(replayQueueEvent('agent-1', AGENT_WATCH_UPDATE_ID, 'agent-2'))
    collector.assertHealthy()
    expect(collector.items).toEqual([])
  })

  it('refuses malformed bytes with the watch label, and keeps the decoder error as the cause', () => {
    let failure: unknown
    try {
      readAgentWatchFrame(Uint8Array.of(0xFF), 'agent-1', 'test watch')
    }
    catch (error) {
      failure = error
    }
    expect(failure).toMatchObject({ message: 'The Worker sent an invalid test watch frame.' })
    expect((failure as Error).cause).toBeInstanceOf(Error)
  })
})

describe('AgentEventCollector', () => {
  it('retains a registration failure and does not accept a later acknowledgement', () => {
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.accept(acknowledgement(AGENT_WATCH_UPDATE_ID, undefined, []))
    collector.accept(acknowledgement())
    expect(collector.subscribed).toBe(false)
    expect(() => collector.assertHealthy()).toThrow('The Worker did not register the test watch')
  })

  it('keeps the selected items in arrival order after the acknowledgement', () => {
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.accept(queueEvent('agent-1', 0n))
    collector.accept(acknowledgement())
    collector.accept(queueEvent('agent-2', 9n))
    collector.accept(queueEvent('agent-1', 2n))
    collector.accept(queueEvent('agent-1', 2n))
    collector.assertHealthy()
    expect(collector.subscribed).toBe(true)
    expect(collector.items).toEqual([0n, 2n, 2n])
  })

  it('records a selector failure without throwing from the stream callback, and ignores later frames', () => {
    const failure = new Error('The selector refused the event.')
    const collector = new AgentEventCollector('agent-1', 'test watch', () => {
      throw failure
    })
    expect(() => collector.accept(queueEvent('agent-1', 1n))).not.toThrow()
    collector.accept(acknowledgement())
    expect(collector.subscribed).toBe(false)
    expect(() => collector.assertHealthy()).toThrow(failure)
  })

  it('never hands an event of another agent to the selector', () => {
    const collector = new AgentEventCollector('agent-1', 'test watch', () => {
      throw new Error('The selector read a foreign event.')
    })
    collector.accept(queueEvent('agent-2', 1n))
    collector.assertHealthy()
    expect(collector.items).toEqual([])
  })

  it('keeps the first failure when a stream end follows it', () => {
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.accept(acknowledgement(AGENT_WATCH_UPDATE_ID, 'agent-1'))
    collector.fail(new Error('The stream ended.'))
    expect(() => collector.assertHealthy()).toThrow('The Worker refused the test watch')
  })

  it('wraps a failure that is not an Error', () => {
    const collector = new AgentEventCollector('agent-1', 'test watch', queueRevision)
    collector.fail('transport closed')
    expect(() => collector.assertHealthy()).toThrow('transport closed')
  })

  it('refuses an empty agent ID', () => {
    expect(() => new AgentEventCollector('', 'test watch', queueRevision)).toThrow('The test watch requires an agent ID.')
  })
})

describe('watchAgentEvents', () => {
  it('sends the subscription, resolves on its acknowledgement, and keeps the selected events', async () => {
    const stream = fakeChannelStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const watching = watchAgentEvents(server, 'agent-1', { label: 'test watch', select: queueRevision })
    await stream.ready
    expect(getTestChannel).toHaveBeenCalledExactlyOnceWith(server.hubUrl, server.adminToken)
    expect(stream.channel.getOrOpenChannel).toHaveBeenCalledExactlyOnceWith(server.workerId)
    const [channelId, method, payload] = stream.channel.stream.mock.calls[0] ?? []
    expect(channelId).toBe('native-channel')
    expect(method).toBe('WatchEvents')
    expect(payload).toEqual(agentWatchRequest('agent-1'))
    stream.message(acknowledgement())
    const watch = await watching
    stream.message(queueEvent('agent-1', 4n))
    expect(watch.items()).toEqual([4n])
    expect(stream.cancel).not.toHaveBeenCalled()
    watch.cancel()
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('fails the read of the items after the stream ends', async () => {
    const stream = fakeChannelStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const watching = watchAgentEvents(server, 'agent-1', { label: 'test watch', select: queueRevision })
    await stream.ready
    stream.message(acknowledgement())
    const watch = await watching
    stream.end()
    expect(() => watch.items()).toThrow('The test watch ended before its assertion.')
  })

  it.each([
    { label: 'an unregistered identity', act: (stream: ReturnType<typeof fakeChannelStream>) => stream.message(acknowledgement(AGENT_WATCH_UPDATE_ID, undefined, [])), message: 'The Worker did not register the test watch' },
    { label: 'a refused subscription', act: (stream: ReturnType<typeof fakeChannelStream>) => stream.message(acknowledgement(AGENT_WATCH_UPDATE_ID, 'agent-1')), message: 'The Worker refused the test watch' },
    { label: 'a stream end', act: (stream: ReturnType<typeof fakeChannelStream>) => stream.end(), message: 'The test watch ended before its assertion.' },
    { label: 'a stream error', act: (stream: ReturnType<typeof fakeChannelStream>) => stream.error(new Error('The native transport disconnected.')), message: 'The native transport disconnected.' },
  ])('rejects $label before the acknowledgement, and cancels the stream', async ({ act, message }) => {
    const stream = fakeChannelStream()
    getTestChannel.mockResolvedValue(stream.channel)
    const watching = watchAgentEvents(server, 'agent-1', { label: 'test watch', select: queueRevision })
    await stream.ready
    act(stream)
    await expect(watching).rejects.toThrow(message)
    expect(stream.cancel).toHaveBeenCalledOnce()
  })

  it('refuses an empty agent ID before it opens a channel', async () => {
    await expect(watchAgentEvents(server, '', { label: 'test watch', select: queueRevision })).rejects.toThrow('requires an agent ID')
    expect(getTestChannel).not.toHaveBeenCalled()
  })
})
