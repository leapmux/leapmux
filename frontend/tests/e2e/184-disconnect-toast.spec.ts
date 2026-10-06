import type { Page } from '@playwright/test'
import type { RecordedToast } from './helpers/toast'
import { clearRecordedToasts, getRecordedToasts } from './helpers/toast'
import { waitForWorkspaceReady } from './helpers/ui'
import { expect, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

/**
 * Record the `leapmux:watch-events-redial` dev events of the page from now on: one for the loss of a WatchEvents
 * stream and one for each failed redial, each sent after the outage announcement decided on that failure.
 * `maxFailures` reads the largest failure count that any worker reported, or 0 before the first report.
 */
async function recordWatchRedials(page: Page): Promise<{ maxFailures: () => Promise<number> }> {
  await page.evaluate(() => {
    const failures: number[] = []
    Object.defineProperty(window, '__e2eWatchRedialFailures', { value: failures, configurable: true })
    window.addEventListener('leapmux:watch-events-redial', (event) => {
      const detail: unknown = event instanceof CustomEvent ? event.detail : undefined
      const count = typeof detail === 'object' && detail !== null ? Reflect.get(detail, 'failures') : undefined
      if (typeof count === 'number')
        failures.push(count)
    })
  })
  return {
    maxFailures: () => page.evaluate(() => {
      const failures: unknown = Reflect.get(window, '__e2eWatchRedialFailures')
      if (!Array.isArray(failures))
        throw new Error('The redial record of this page is gone: a navigation replaced the page after it started.')
      return Math.max(0, ...failures.map(Number))
    }),
  }
}

/**
 * Check the message that reports a lost worker connection.
 * Mobile app or tab changes can close a socket. Previously, one outage could produce separate channel-not-open and channel-disconnected messages.
 * The app now reports one outage only after reconnection attempts fail.
 * Stop the worker to reproduce that path deterministically. The hub closes its channel, the watch stream fails, and later connection attempts cannot reach the worker.
 */
test.describe('Disconnection toasts', () => {
  /** Every toast the app raised whose text names a channel-layer internal. */
  function jargonToasts(toasts: RecordedToast[]) {
    return toasts.filter(t => /channel (?:not open|disconnected|closed)/i.test(t.message))
  }

  /** Every toast the app raised to announce the outage. */
  function outageToasts(toasts: RecordedToast[]) {
    return toasts.filter(t => t.message.includes('Connection to worker lost'))
  }

  test('announces a worker outage once, in the app\'s own words', async ({ page, authenticatedWorkspace, separateHubWorker }) => {
    void authenticatedWorkspace
    await waitForWorkspaceReady(page)
    // Only failures caused by the stop below are interesting; anything the
    // workspace raised while loading is not.
    await clearRecordedToasts(page)
    const redials = await recordWatchRedials(page)

    // Start timing before the stop request. Process shutdown time belongs to the observed outage interval.
    const killedAt = Date.now()
    await stopWorker(separateHubWorker)
    await waitForWorkerOffline(separateHubWorker)

    await expect.poll(async () => outageToasts(await getRecordedToasts(page)).length).toBe(1)

    const announced = outageToasts(await getRecordedToasts(page))[0]!
    // The grace period, measured. Two quiet redials sit 1s and 2s after the app
    // notices, and the backoff jitters each by at most 20%, so the earliest an
    // honest announcement can land is 2.4s after that. A gate that announced the
    // first failure lands as soon as the app notices instead.
    expect(announced.timestamp - killedAt).toBeGreaterThan(2000)
    expect(announced.variant).toBe('danger')

    // The redials keep failing and the backoff keeps climbing. Neither may add a
    // second announcement. The app reports each failure after the announcement
    // decided on it, so two more failures than at the announcement are two more
    // chances to announce, and the toasts read after them are final for both.
    const failuresAtAnnouncement = await redials.maxFailures()
    await expect.poll(() => redials.maxFailures(), 'two more redials failed after the announcement').toBeGreaterThanOrEqual(failuresAtAnnouncement + 2)
    const settled = await getRecordedToasts(page)
    expect(outageToasts(settled)).toHaveLength(1)
    expect(jargonToasts(settled), 'no toast may name a channel-layer internal').toHaveLength(0)
  })
})
