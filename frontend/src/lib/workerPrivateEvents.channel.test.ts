import type { ChannelManager } from './channel'
import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { WorkerPrivateEventSchema } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { channelInternals, ChannelManagerTestHarness } from './channel.test-support'
import { STREAM_HEALTHY_AFTER_MS } from './retry'

// These tests run the subscriber on a real ChannelManager, because the decision
// that they pin spans both: the manager decodes each frame, and the subscriber
// owns the reconnect that a decode failure must reach.
const real = vi.hoisted(() => ({ manager: undefined as ChannelManager | undefined }))

function manager(): ChannelManager {
  if (!real.manager)
    throw new Error('The test must open its channel manager first.')
  return real.manager
}

vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    getOrOpenChannel: (workerId: string) => manager().getOrOpenChannel(workerId),
    stream: (...args: Parameters<ChannelManager['stream']>) => manager().stream(...args),
  },
}))

const { openWorkerPrivateEventStream } = await import('./workerPrivateEvents')

function tabRenamedFrame(tabId: string): Uint8Array {
  return toBinary(WorkerPrivateEventSchema, create(WorkerPrivateEventSchema, {
    event: {
      case: 'tabRenamed',
      value: {
        tabId,
        tabType: TabType.AGENT,
        title: 'Renamed',
        originClientId: 'c',
      },
    },
  }))
}

describe('openWorkerPrivateEventStream on a real channel', () => {
  const h = new ChannelManagerTestHarness()
  let channelId: string

  beforeEach(async () => {
    h.setup()
    real.manager = h.mgr
    channelId = await h.openTestChannel('w1')
    vi.useFakeTimers()
    // Math.random == 0.5 makes the jitter offset zero, so the first reconnect
    // comes after exactly the initial 250ms.
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
    h.teardown()
    real.manager = undefined
  })

  function liveStreamIds(): number[] {
    return [
      ...channelInternals(h.mgr, channelId).streamListeners.keys(),
    ]
  }

  it('reconnects after a frame that does not decode, and the new stream delivers events', async () => {
    const renamed = vi.fn()
    const stop = openWorkerPrivateEventStream({ workerId: 'w1', onTabRenamed: renamed })
    await vi.advanceTimersByTimeAsync(0)
    const [first] = liveStreamIds()
    if (first === undefined)
      throw new Error('The subscriber must open a stream.')

    h.sendStreamMessageFromWorker(channelId, first, Uint8Array.of(0xFF))

    expect(liveStreamIds(), 'a decode failure ends the stream').toEqual([])
    await vi.advanceTimersByTimeAsync(250)
    const [second] = liveStreamIds()
    expect(second).toBeDefined()
    expect(second).not.toBe(first)

    h.sendStreamMessageFromWorker(channelId, second!, tabRenamedFrame('tab-1'))
    expect(renamed).toHaveBeenCalledExactlyOnceWith({
      tabId: 'tab-1',
      tabType: TabType.AGENT,
      title: 'Renamed',
      originClientId: 'c',
    })
    stop()
  })

  // A frame that fails to decode proves nothing about the stream, so a worker
  // that sends only bad frames must keep backing off.
  it('keeps the backoff streak after a frame that does not decode', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w1',
      onTabRenamed: () => {},
    })
    await vi.advanceTimersByTimeAsync(0)
    h.sendStreamMessageFromWorker(channelId, liveStreamIds()[0]!, Uint8Array.of(0xFF))
    await vi.advanceTimersByTimeAsync(250)
    h.sendStreamMessageFromWorker(channelId, liveStreamIds()[0]!, Uint8Array.of(0xFF))

    await vi.advanceTimersByTimeAsync(499)
    expect(liveStreamIds(), 'the second reconnect waits for the doubled delay').toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(liveStreamIds()).toHaveLength(1)
    stop()
  })

  /** Deliver a frame that decodes, then one that fails the stream. */
  function sendGoodThenBadFrame(): void {
    const [stream] = liveStreamIds()
    if (stream === undefined)
      throw new Error('The subscriber must hold a stream.')
    h.sendStreamMessageFromWorker(channelId, stream, tabRenamedFrame('tab-1'))
    h.sendStreamMessageFromWorker(channelId, stream, Uint8Array.of(0xFF))
    expect(liveStreamIds(), 'the bad frame ends the stream').toEqual([])
  }

  // A worker that replays a bad frame delivers the frames in front of it on
  // each attempt. A good frame must not reset the streak, or the reconnect comes
  // at the minimum delay for the life of the page.
  it('keeps the backoff streak when a good frame precedes the bad one', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w1',
      onTabRenamed: () => {},
    })
    await vi.advanceTimersByTimeAsync(0)

    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(250)
    expect(liveStreamIds()).toHaveLength(1)

    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(499)
    expect(liveStreamIds(), 'the second reconnect waits for the doubled delay').toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(liveStreamIds()).toHaveLength(1)

    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(999)
    expect(liveStreamIds(), 'the third reconnect doubles again').toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(liveStreamIds()).toHaveLength(1)
    stop()
  })

  it('starts a new streak after a stream that stayed open for the healthy window', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w1',
      onTabRenamed: () => {},
    })
    await vi.advanceTimersByTimeAsync(0)
    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(250)
    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(500)
    expect(liveStreamIds()).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(STREAM_HEALTHY_AFTER_MS)
    sendGoodThenBadFrame()

    await vi.advanceTimersByTimeAsync(249)
    expect(liveStreamIds()).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(liveStreamIds(), 'the streak restarts at the minimum delay').toHaveLength(1)
    stop()
  })

  it('keeps the streak of a stream that ended before the healthy window closed', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w1',
      onTabRenamed: () => {},
    })
    await vi.advanceTimersByTimeAsync(0)
    sendGoodThenBadFrame()
    await vi.advanceTimersByTimeAsync(250)
    expect(liveStreamIds()).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(STREAM_HEALTHY_AFTER_MS - 1)
    sendGoodThenBadFrame()

    await vi.advanceTimersByTimeAsync(499)
    expect(liveStreamIds(), 'one millisecond short of the window proves nothing').toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(liveStreamIds()).toHaveLength(1)
    stop()
  })

  // The try around the dispatch exists for this case alone: the stream stays
  // healthy, so a callback that throws costs no reconnect.
  it('keeps the stream when a callback throws', async () => {
    const renamed = vi.fn(() => {
      throw new Error('callback bug')
    })
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const stop = openWorkerPrivateEventStream({ workerId: 'w1', onTabRenamed: renamed })
    await vi.advanceTimersByTimeAsync(0)
    const [only] = liveStreamIds()

    h.sendStreamMessageFromWorker(channelId, only!, tabRenamedFrame('tab-1'))
    h.sendStreamMessageFromWorker(channelId, only!, tabRenamedFrame('tab-2'))

    expect(renamed).toHaveBeenCalledTimes(2)
    expect(liveStreamIds()).toEqual([only])
    expect(warn).toHaveBeenCalled()
    stop()
  })
})
