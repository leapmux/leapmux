import { create, toBinary } from '@bufbuild/protobuf'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkerPrivateEventSchema } from '~/generated/leapmux/v1/worker_private_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'

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
  emitMessage: (payload: Uint8Array) => void
  emitError: (err: Error) => void
  emitEnd: () => void
}

let nextReq = 0

function makeFakeStream(): { handle: unknown, control: FakeStream } {
  let onMsg: ((m: { payload: Uint8Array }) => void) | undefined
  let onEnd: (() => void) | undefined
  let onErr: ((err: Error) => void) | undefined
  const requestId = `req-${nextReq++}`
  const cancel = vi.fn()
  const handle = {
    requestId,
    cancel,
    onMessage: (cb: (m: { payload: Uint8Array }) => void) => { onMsg = cb },
    onEnd: (cb: () => void) => { onEnd = cb },
    onError: (cb: (err: Error) => void) => { onErr = cb },
  }
  return {
    handle,
    control: {
      requestId,
      cancel,
      emitMessage: p => onMsg?.({ payload: p }),
      emitError: e => onErr?.(e),
      emitEnd: () => onEnd?.(),
    },
  }
}

function encodeTabRenamed(tabId: string): Uint8Array {
  const evt = create(WorkerPrivateEventSchema, {
    event: { case: 'tabRenamed', value: { tabId, tabType: TabType.UNSPECIFIED, title: 'T', originClientId: 'c' } },
  })
  return toBinary(WorkerPrivateEventSchema, evt)
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

  it('backs off exponentially on repeated failures and resets after a healthy message', async () => {
    const stop = openWorkerPrivateEventStream({ workerId: 'w', onTabRenamed: () => {} })
    await tick(0)
    expect(stream).toHaveBeenCalledTimes(1)

    // First drop schedules a reconnect at 250ms.
    streams[0].emitError(new Error('drop'))
    await tick(249)
    expect(stream).toHaveBeenCalledTimes(1)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(2)

    // Second consecutive drop doubles the delay to 500ms.
    streams[1].emitError(new Error('drop'))
    await tick(499)
    expect(stream).toHaveBeenCalledTimes(2)
    await tick(1)
    expect(stream).toHaveBeenCalledTimes(3)

    // A healthy message resets the streak, so the next reconnect is 250ms again
    // (not 1000ms).
    const renamed: string[] = []
    stop()
    const stop2 = openWorkerPrivateEventStream({
      workerId: 'w',
      onTabRenamed: (e: { tabId: string }) => renamed.push(e.tabId),
    })
    await tick(0)
    const baseline = stream.mock.calls.length
    streams[streams.length - 1].emitMessage(encodeTabRenamed('tab-1'))
    expect(renamed).toEqual(['tab-1'])
    // Grow the streak once, then a healthy message, then drop again.
    streams[streams.length - 1].emitError(new Error('drop'))
    await tick(250)
    expect(stream).toHaveBeenCalledTimes(baseline + 1)
    streams[streams.length - 1].emitMessage(encodeTabRenamed('tab-2'))
    streams[streams.length - 1].emitError(new Error('drop'))
    // After the reset, the next delay is the initial 250ms, not the doubled 500.
    await tick(250)
    expect(stream).toHaveBeenCalledTimes(baseline + 2)

    stop2()
  })

  it('teardown calls cancel on the stream handle (#337)', async () => {
    const stop = openWorkerPrivateEventStream({ workerId: 'w', onTabRenamed: () => {} })
    await tick(0)
    expect(streams[0].cancel).toHaveBeenCalledTimes(0)
    stop()
    expect(streams[0].cancel).toHaveBeenCalledOnce()
  })

  it('teardown wakes a parked loop so it does not linger on a clean close with a live channel', async () => {
    // Regression: handle.cancel() detaches the stream listener synchronously,
    // so onEnd/onError never fire and the loop's first `await new Promise`
    // never resolved — the closure stayed alive until a transport error
    // eventually fired. teardown must wake the parked loop directly.
    const stop = openWorkerPrivateEventStream({ workerId: 'w', onTabRenamed: () => {} })
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
    const stop = openWorkerPrivateEventStream({ workerId: 'w', onTabRenamed: () => {} })
    await tick(0)
    expect(stream).toHaveBeenCalledTimes(1)

    // Drop the stream so a reconnect timer is armed, then tear down before it
    // fires. cancelAll() must cancel the pending retry.
    streams[0].emitError(new Error('drop'))
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
    const stop = openWorkerPrivateEventStream({ workerId: 'w', onTabRenamed: (e: { tabId: string }) => renamed.push(e.tabId) })
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
