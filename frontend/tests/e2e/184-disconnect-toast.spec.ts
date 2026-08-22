import type { RecordedToast } from './helpers/toast'
import { clearRecordedToasts, getRecordedToasts } from './helpers/toast'
import { waitForWorkspaceReady } from './helpers/ui'
import { expect, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

/**
 * What the user reads when the link to a worker drops.
 *
 * A mobile browser drops the socket every time the user leaves for another app
 * or another tab, and the app used to answer that with TWO error toasts, both
 * naming our own plumbing: "channel not open" from whichever call raced the
 * teardown, and "channel disconnected" from the drained watch stream. One outage
 * now produces one sentence, and only after the redials have really failed.
 *
 * Driven by stopping the worker rather than by pulling the browser offline: it
 * is the one drop this suite can produce deterministically, and it exercises the
 * same path -- the hub tears down the channel, the watch stream errors, and the
 * redials fail at the hub leg because the worker is gone.
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

    // Marked BEFORE the stop, because stopWorker sleeps two seconds after the
    // signal. Marking after it would charge that sleep against the grace period
    // measured below and leave the assertion almost no margin.
    const killedAt = Date.now()
    await stopWorker()
    await waitForWorkerOffline(separateHubWorker.hubUrl, separateHubWorker.adminToken)

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
