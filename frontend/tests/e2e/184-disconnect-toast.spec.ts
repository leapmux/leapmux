import type { RecordedToast } from './helpers/toast'
import { clearRecordedToasts, getRecordedToasts } from './helpers/toast'
import { waitForWorkspaceReady } from './helpers/ui'
import { expect, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

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
    // second announcement.
    await page.waitForTimeout(15_000)
    const settled = await getRecordedToasts(page)
    expect(outageToasts(settled)).toHaveLength(1)
    expect(jargonToasts(settled), 'no toast may name a channel-layer internal').toHaveLength(0)
  })
})
