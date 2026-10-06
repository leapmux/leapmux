import type { ChannelManager } from '~/lib/channel'
import { create } from '@bufbuild/protobuf'
import { vi } from 'vitest'
import { InnerStreamMessageSchema } from '~/generated/proto/leapmux/v1/channel_pb'

type ChannelStream = ReturnType<ChannelManager['stream']>

/**
 * A channel whose one stream the test drives: it delivers each frame, error, and
 * end that the test states, and records each cancel.
 *
 * `ready` resolves once the code under test registered its end listener, which
 * it does last, so a test that awaits it delivers no frame to a missing listener.
 */
export function fakeChannelStream() {
  let onMessage: Parameters<ChannelStream['onMessage']>[0] | undefined
  let onError: Parameters<ChannelStream['onError']>[0] | undefined
  let onEnd: Parameters<ChannelStream['onEnd']>[0] | undefined
  let signalReady: (() => void) | undefined
  const ready = new Promise<void>((resolve) => {
    signalReady = resolve
  })
  const cancel = vi.fn()
  const watch = {
    requestId: 1,
    onMessage: (listener: Parameters<ChannelStream['onMessage']>[0]) => {
      onMessage = listener
    },
    onError: (listener: Parameters<ChannelStream['onError']>[0]) => {
      onError = listener
    },
    onEnd: (listener: Parameters<ChannelStream['onEnd']>[0]) => {
      onEnd = listener
      signalReady?.()
    },
    cancel,
    send: vi.fn(),
  } satisfies ChannelStream
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
