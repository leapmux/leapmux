/* eslint-disable no-console */
/**
 * Measure a cold mobile browser against the suite Hub on an LTE link.
 * Record phase order and byte counts. The test sets no elapsed-time limit.
 */
import type { CDPSession } from '@playwright/test'
import type { StartupReport } from './helpers/startupTiming'
import { expect, test } from './fixtures'
import { withCleanup } from './helpers/cleanup'
import {
  attachResponseSizeListener,
  buildPhaseMarks,
  collectStartupResources,
  installNetworkThrottle,
  installStartupObservers,
  LTE_NETWORK_PROFILE,
  readStartupMarks,
  renderStartupReport,
  STARTUP_BUCKETS,
  sumBytesBeforeShell,
} from './helpers/startupTiming'
import { COARSE_POINTER_METRICS } from './helpers/touch'
import { appMenuTrigger, loginViaToken } from './helpers/ui'

test.describe('mobile LTE cold-start timing', () => {
  test.describe.configure({ retries: 0 })

  test.use(COARSE_POINTER_METRICS)

  // The `workspace` fixture gives the app a workspace with an agent to boot into, and deletes it after the test.
  test('traces phase + byte ranking before shell_visible', async ({ page, leapmuxServer: srv, workspace }, testInfo) => {
    void workspace
    let cdp: CDPSession | undefined
    await withCleanup(async () => {
      cdp = await installNetworkThrottle(page, LTE_NETWORK_PROFILE)
      await loginViaToken(page, srv.adminToken)
      await installStartupObservers(page)

      const sizeListener = attachResponseSizeListener(page)

      // Cold authenticated boot: cookie already set, first document navigation.
      await page.goto('/', { waitUntil: 'commit' })

      const shell = appMenuTrigger(page)
      await expect(shell).toBeVisible()
      sizeListener.markShellVisible()

      const shellVisibleMs = await page.evaluate(() => performance.now())
      const marks = await readStartupMarks(page, shellVisibleMs)
      const resources = await collectStartupResources(
        page,
        shellVisibleMs,
        sizeListener.sizes,
        sizeListener.sizesBeforeShell,
      )
      const { bytes, counts } = sumBytesBeforeShell(resources)

      const report: StartupReport = {
        profileLabel: LTE_NETWORK_PROFILE.label,
        marks,
        phases: buildPhaseMarks(marks),
        resources,
        bytesBeforeShell: bytes,
        countBeforeShell: counts,
      }
      const text = renderStartupReport(report)
      console.log(`\n──── mobile LTE cold-start timing ────\n${text}\n──────────────────────────────────────\n`)
      await testInfo.attach('startup-timing.txt', { body: text, contentType: 'text/plain' })
      // Attach the measured ranking to the report.
      await testInfo.attach('startup-timing-ranked.json', {
        body: JSON.stringify({
          profile: report.profileLabel,
          shellVisibleMs: marks.shellVisible,
          bytesBeforeShell: bytes,
          countBeforeShell: counts,
          phases: report.phases,
        }, null, 2),
        contentType: 'application/json',
      })

      // Check report structure and completed bytes.
      expect(marks.shellVisible, 'shell_visible mark').toBeGreaterThan(0)
      for (const bucket of STARTUP_BUCKETS) {
        expect(bytes, `bucket ${bucket} present`).toHaveProperty(bucket)
        expect(counts, `count ${bucket} present`).toHaveProperty(bucket)
      }
      const totalBytes = STARTUP_BUCKETS.reduce((s, b) => s + bytes[b], 0)
      expect(totalBytes, 'some bytes finished before the shell').toBeGreaterThan(0)

      // The document does not preload fonts. A code view can fetch a font after the shell appears.
      expect(bytes.fonts, 'no font preload on the critical path').toBe(0)

      // The render Workers load after shell_visible.
      expect(bytes.workers, 'no render workers on the critical path').toBe(0)

      // The static HTML or Suspense fallback displays the splash before the client modules load.
      expect(
        marks.appDivNonempty,
        'app_div_nonempty should fire from the static boot splash',
      ).not.toBeNull()
      expect(marks.appDivNonempty!).toBeLessThan(marks.shellVisible)

      // The classifier assigns each URL before the shell to a declared bucket.
      for (const r of resources.filter(x => x.beforeShell))
        expect(STARTUP_BUCKETS, r.url).toContain(r.bucket)
    }, async () => {
      await cdp?.detach()
    })
  })
})
