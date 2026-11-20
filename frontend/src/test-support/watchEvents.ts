import type { WatchEventsHandle } from '~/api/workerRpc'
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
