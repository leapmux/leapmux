/* eslint-disable no-console */
/**
 * Cold authenticated boot on an emulated LTE link (mobile viewport).
 *
 * Ranks what finishes before the shell mounts — fonts, entry JS,
 * modulepreloaded chunks, the (app) route, workers, RPCs — so critical-path
 * cuts are ordered by measured cost rather than by desktop Network panels.
 *
 * Absolute milliseconds vary by host CPU; the byte ranking and phase order
 * are the decision input. This spec does not gate on a wall-clock budget.
 */
import type { StartupReport } from './helpers/startupTiming'
import { expect, test } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { stopDevServer } from './helpers/devServer'
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
import { startTimingServer } from './helpers/timingFixture'
import { COARSE_POINTER_METRICS } from './helpers/touch'
import { loginViaToken } from './helpers/ui'

/** Same shell oracle as loginViaUI: AppShell is up behind AuthGuard. */
function shellTrigger(page: import('@playwright/test').Page) {
  return page.getByTestId('app-menu-trigger').first().or(page.getByTestId('collapsed-new-tab-button').first())
}

test.describe('mobile LTE cold-start timing', () => {
  test.describe.configure({ retries: 0 })

  let srv: Awaited<ReturnType<typeof startTimingServer>>

  test.beforeAll(async () => {
    srv = await startTimingServer({
      dataDirPrefix: 'leapmux-startup-timing-e2e',
      env: {
        LEAPMUX_WORKER_NAME: 'Local',
      },
    })
  })

  test.afterAll(async () => {
    if (srv)
      await stopDevServer(srv)
  })

  test('traces phase + byte ranking before shell_visible', async ({ browser }, testInfo) => {
    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `startup-${Date.now()}`,
    )
    await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)

    const context = await browser.newContext({
      baseURL: srv.hubUrl,
      ...COARSE_POINTER_METRICS,
    })
    const page = await context.newPage()
    const cdp = await installNetworkThrottle(page, LTE_NETWORK_PROFILE)
    await loginViaToken(page, srv.adminToken)
    await installStartupObservers(page)

    const sizeListener = attachResponseSizeListener(page)

    // Cold authenticated boot: cookie already set, first document navigation.
    await page.goto('/', { waitUntil: 'commit' })

    const shell = shellTrigger(page)
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
    // Also land under artifacts for the cloud walkthrough path.
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

    // Structural honesty checks — not wall-clock budgets.
    expect(marks.shellVisible, 'shell_visible mark').toBeGreaterThan(0)
    for (const bucket of STARTUP_BUCKETS) {
      expect(bytes, `bucket ${bucket} present`).toHaveProperty(bucket)
      expect(counts, `count ${bucket} present`).toHaveProperty(bucket)
    }
    const totalBytes = STARTUP_BUCKETS.reduce((s, b) => s + bytes[b], 0)
    expect(totalBytes, 'some bytes finished before the shell').toBeGreaterThan(0)

    // Regular Hack NF alone was ~50% of bytes-before-shell on LTE; document
    // font preloads are gone. A code surface may still fetch a face later.
    expect(bytes.fonts, 'no font preload on the critical path').toBe(0)

    // Splash must be in the static HTML (or Suspense fallback) so the page is
    // never a blank #app while the CSR graph downloads.
    expect(
      marks.appDivNonempty,
      'app_div_nonempty should fire from the static boot splash',
    ).not.toBeNull()
    expect(marks.appDivNonempty!).toBeLessThan(marks.shellVisible)

    // Every tracked before-shell URL must classify into a known bucket (the
    // classifier has no escape hatch beyond `other`).
    for (const r of resources.filter(x => x.beforeShell))
      expect(STARTUP_BUCKETS, r.url).toContain(r.bucket)

    await deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId).catch(() => {})
    await cdp.detach().catch(() => {})
    await context.close()
  })
})
