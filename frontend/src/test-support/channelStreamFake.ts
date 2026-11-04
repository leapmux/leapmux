import type { MessageInitShape } from '@bufbuild/protobuf'
import type { WatchEventsRequestSchema, WatchEventsResponse, WorkerWatchService } from '~/generated/proto/leapmux/v1/watch_pb'
import type { ChannelStream } from '~/lib/channel'
import { fromBinary, toBinary } from '@bufbuild/protobuf'
import { vi } from 'vitest'
import { WatchEventsResponseSchema } from '~/generated/proto/leapmux/v1/watch_pb'

type WatchEventsStream = ChannelStream<typeof WatchEventsRequestSchema, typeof WatchEventsResponseSchema>

/** `ChannelManager.stream` for the one method that this fake serves. */
type OpenWatchEventsStream = (
  channelId: string,
  method: typeof WorkerWatchService.method.watchEvents,
  request: MessageInitShape<typeof WatchEventsRequestSchema>,
) => WatchEventsStream

/**
 * A channel whose one stream the test drives: it delivers each frame, error, and
 * end that the test states, and records each cancel.
 *
 * `ready` resolves once the code under test registered its end listener, which
 * it does last, so a test that awaits it delivers no frame to a missing listener.
 */
export function fakeChannelStream() {
  let onMessage: Parameters<WatchEventsStream['onMessage']>[0] | undefined
  let onError: Parameters<WatchEventsStream['onError']>[0] | undefined
  let onEnd: Parameters<WatchEventsStream['onEnd']>[0] | undefined
  let signalReady: (() => void) | undefined
  const ready = new Promise<void>((resolve) => {
    signalReady = resolve
  })
  const cancel = vi.fn()
  const watch = {
    requestId: 1,
    onMessage: (listener: Parameters<WatchEventsStream['onMessage']>[0]) => {
      onMessage = listener
    },
    onError: (listener: Parameters<WatchEventsStream['onError']>[0]) => {
      onError = listener
    },
    onEnd: (listener: Parameters<WatchEventsStream['onEnd']>[0]) => {
      onEnd = listener
      signalReady?.()
    },
    cancel,
    send: vi.fn(),
  } satisfies WatchEventsStream
  const channel = {
    getOrOpenChannel: vi.fn(async () => 'native-channel'),
    stream: vi.fn<OpenWatchEventsStream>(() => watch),
  }
  return {
    channel,
    watch,
    ready,
    cancel,
    /** Deliver one response through its wire form, as a real channel stream does. */
    message: (response: WatchEventsResponse) => {
      if (!onMessage)
        throw new Error('The test stream has no message handler.')
      onMessage(fromBinary(WatchEventsResponseSchema, toBinary(WatchEventsResponseSchema, response)))
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
