import { expect, vi } from 'vitest'
import { READINESS_REQUEST_LIMIT_MS } from '~/components/chat/renderReadiness'

/**
 * Assert that a row copy stops waiting exactly at the limit of one request
 * (READINESS_REQUEST_LIMIT_MS) that began `elapsedMs` ago: it still waits one
 * millisecond before the limit, and is measurable at the limit. Advances the fake
 * timers of the test to the limit.
 */
export async function expectReleasedAtRequestLimit(scope: { measurable: () => boolean }, elapsedMs = 0): Promise<void> {
  await vi.advanceTimersByTimeAsync(READINESS_REQUEST_LIMIT_MS - elapsedMs - 1)
  expect(scope.measurable()).toBe(false)
  await vi.advanceTimersByTimeAsync(1)
  expect(scope.measurable()).toBe(true)
}
