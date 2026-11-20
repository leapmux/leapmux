import type { WatchEventsHandle } from '~/api/workerRpc'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { vi } from 'vitest'

/** Provide the identity of a mock watch stream to tests that deliver frames directly. */
export function createTestWatchHandle(): WatchEventsHandle {
  return {
    onEvent: vi.fn(),
    onEnd: vi.fn(),
    onError: vi.fn(),
    update: vi.fn(),
    close: vi.fn(),
  }
}

/** Capture one suite's watch callbacks so its frames reach the real dispatcher. */
export const testWatchStreams = (() => {
  let opts: UseWatchEventsStreamsOpts | undefined
  let handle: WatchEventsHandle | undefined
  const getOptions = () => {
    if (!opts)
      throw new Error('useWorkspaceConnection did not open its watch streams')
    return opts
  }
  return {
    getOptions,
    reset() {
      opts = undefined
    },
    useWatchEventsStreams(options: UseWatchEventsStreamsOpts) {
      opts = options
      return {
        abortSignalFor: () => undefined,
      }
    },
    deliverFor(workerId: string) {
      return (frame: Parameters<UseWatchEventsStreamsOpts['onEvent']>[1]): void => {
        handle ??= createTestWatchHandle()
        getOptions().onEvent(workerId, frame, handle)
      }
    },
  }
})()
