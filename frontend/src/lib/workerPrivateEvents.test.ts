import type { WorkerPrivateEvent } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { create } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { WorkerPrivateEventSchema, WorkerPrivateService } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { STREAM_HEALTHY_AFTER_MS } from './retry'

// Mock the E2EE channel transport so we can drive connect / message / error
// entirely from the test.
vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    getOrOpenChannel: vi.fn(),
    stream: vi.fn(),
    removeStreamListener: vi.fn(),
  },
}))

const { channelManager } = await import('~/api/workerRpc')
const { openWorkerPrivateEventStream } = await import('./workerPrivateEvents')

interface FakeStream {
  requestId: string
  cancel: ReturnType<typeof vi.fn>
  emitMessage: (event: WorkerPrivateEvent) => void
  emitError: (err: Error) => void
  emitEnd: () => void
}

let nextReq = 0

function makeFakeStream(): { handle: unknown, control: FakeStream } {
  let onMsg: ((event: WorkerPrivateEvent) => void) | undefined
  let onEnd: (() => void) | undefined
  let onErr: ((err: Error) => void) | undefined
  const requestId = `req-${nextReq++}`
  const cancel = vi.fn()
  const handle = {
    requestId,
    cancel,
    onMessage: (cb: (event: WorkerPrivateEvent) => void) => { onMsg = cb },
    onEnd: (cb: () => void) => { onEnd = cb },
    onError: (cb: (err: Error) => void) => { onErr = cb },
  }
  return {
    handle,
    control: {
      requestId,
      cancel,
      emitMessage: event => onMsg?.(event),
      emitError: e => onErr?.(e),
      emitEnd: () => onEnd?.(),
    },
  }
}

function tabRenamed(tabId: string): WorkerPrivateEvent {
  return create(WorkerPrivateEventSchema, {
    event: {
      case: 'tabRenamed',
      value: {
        tabId,
        tabType: TabType.UNSPECIFIED,
        title: 'T',
        originClientId: 'c',
      },
    },
  })
}

describe('openWorkerPrivateEventStream reconnect backoff', () => {
  const getOrOpenChannel = vi.mocked(channelManager.getOrOpenChannel)
  const stream = vi.mocked(channelManager.stream)
  const removeStreamListener = vi.mocked(channelManager.removeStreamListener)
  let streams: FakeStream[]

  beforeEach(() => {
    vi.useFakeTimers()
    // Math.random == 0.5 makes the symmetric jitter offset exactly zero, so
    // the scheduled delay equals the un-jittered base and we can assert an
    // exact 250 / 500 / 1000 sequence.
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    streams = []
    getOrOpenChannel.mockReset()
    stream.mockReset()
    removeStreamListener.mockReset()
    getOrOpenChannel.mockResolvedValue('chan')
    stream.mockImplementation(() => {
      const s = makeFakeStream()
      streams.push(s.control)
      return s.handle as ReturnType<typeof channelManager.stream>
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  // Advance fake time and flush the async connect chain (getOrOpenChannel +
  // stream()).
  async function tick(ms: number) {
    await vi.advanceTimersByTimeAsync(ms)
    await vi.advanceTimersByTimeAsync(0)
  }

  // Stream lookup: each test opens exactly the streams it expects, so a
  // missing entry is a harness failure, not a case to guard.
  function streamAt(index: number): FakeStream {
    const s = streams[index]
    if (s === undefined)
      throw new Error(`expected a fake stream at index ${index}`)
    return s
  }

  // The stream travels under the generated descriptor, so the procedure on
  // the wire matches the handler the worker registers.
  it('opens the stream on the worker\'s channel under the WatchWorkerPrivateEvents descriptor', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: () => {},
    })
    await tick(0)

    expect(getOrOpenChannel).toHaveBeenCalledWith('w')
    expect(stream).toHaveBeenCalledOnce()
    expect(stream).toHaveBeenCalledWith('chan', WorkerPrivateService.method.watchWorkerPrivateEvents, {})
    stop()
  })

  it('backs off exponentially on repeated failures and resets after the healthy window', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: () => {},
    })
    await tick(0)
    expect(stream).toHaveBeenCalledTimes(1)

    // First drop schedules a reconnect at 250ms.
    streamAt(0).emitError(new Error('drop'))
    await tick(249)
    expect(stream).toHaveBeenCalledTimes(1)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(2)

    // Second consecutive drop doubles the delay to 500ms.
    streamAt(1).emitError(new Error('drop'))
    await tick(499)
    expect(stream).toHaveBeenCalledTimes(2)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(3)

    // A stream that stays open for the healthy window ends the streak, so the
    // next reconnect is 250ms again (not 1000ms). It delivered no message: a
    // quiet stream is as healthy as a busy one.
    await tick(STREAM_HEALTHY_AFTER_MS)
    streamAt(2).emitError(new Error('drop'))
    await tick(249)
    expect(stream).toHaveBeenCalledTimes(3)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(4)

    stop()
  })

  // Only `streamProvedHealthy` ends a streak, so a stream that delivers a message
  // and then drops at once keeps backing off.
  it('keeps the streak of a stream that drops soon after a message', async () => {
    const renamed: string[] = []
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: (e: { tabId: string }) => renamed.push(e.tabId),
    })
    await tick(0)
    streamAt(0).emitMessage(tabRenamed('tab-1'))
    streamAt(0).emitError(new Error('drop'))
    await tick(250)
    expect(stream).toHaveBeenCalledTimes(2)

    streamAt(1).emitMessage(tabRenamed('tab-2'))
    streamAt(1).emitError(new Error('drop'))
    await tick(499)
    expect(stream, 'the message did not restart the delay at 250ms').toHaveBeenCalledTimes(2)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(3)

    expect(renamed).toEqual(['tab-1', 'tab-2'])
    stop()
  })

  it('teardown calls cancel on the stream handle (#337)', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: () => {},
    })
    await tick(0)
    expect(streamAt(0).cancel).toHaveBeenCalledTimes(0)
    stop()
    expect(streamAt(0).cancel).toHaveBeenCalledOnce()
  })

  it('teardown wakes a parked loop so it does not linger on a clean close with a live channel', async () => {
    // Regression: handle.cancel() detaches the stream listener synchronously,
    // so onEnd/onError never fire and the loop's first `await new Promise`
    // never resolved — the closure stayed alive until a transport error
    // eventually fired. teardown must wake the parked loop directly.
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: () => {},
    })
    await tick(0)
    expect(stream).toHaveBeenCalledTimes(1)

    // Clean teardown: cancel the handle without emitting end/error. The loop
    // is parked in the stream-wait Promise.
    stop()
    // Advance timers: with the fix, the loop observed the wake and exited
    // (stopped=true short-circuits). Without the fix, the loop would still be
    // parked and a reconnect could fire after the backoff window.
    await tick(10_000)
    // No new stream should be opened: teardown set stopped=true and woke the
    // parked loop, so the reconnect backoff branch never runs.
    expect(stream).toHaveBeenCalledTimes(1)
  })

  it('stops reconnecting after teardown', async () => {
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: () => {},
    })
    await tick(0)
    expect(stream).toHaveBeenCalledTimes(1)

    // Drop the stream so a reconnect timer is armed, then tear down before it
    // fires. cancelAll() must cancel the pending retry.
    streamAt(0).emitError(new Error('drop'))
    stop()
    await tick(10_000)
    expect(stream).toHaveBeenCalledTimes(1)
  })

  it('does not open a stream when torn down while the channel is still connecting', async () => {
    // Hold getOrOpenChannel pending so teardown can race the in-flight connect,
    // exactly when currentClose is still null and the returned cleanup has
    // nothing to close.
    let resolveChannel: (id: string) => void = () => {}
    getOrOpenChannel.mockReset()
    getOrOpenChannel.mockReturnValue(new Promise<string>((res) => {
      resolveChannel = res
    }))

    const renamed: string[] = []
    const stop = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: (e: { tabId: string }) => renamed.push(e.tabId),
    })
    await tick(0)
    // Still awaiting the channel open: no stream registered yet.
    expect(stream).toHaveBeenCalledTimes(0)

    // Tear down mid-connect, then let the channel finish opening.
    stop()
    resolveChannel('chan')
    await tick(0)

    // The disposed guard must bail before registering the stream listener --
    // otherwise the subscription would linger and keep firing callbacks into a
    // torn-down caller until the stream errored on its own.
    expect(stream).toHaveBeenCalledTimes(0)
    expect(removeStreamListener).not.toHaveBeenCalled()
    expect(renamed).toEqual([])
  })
})
