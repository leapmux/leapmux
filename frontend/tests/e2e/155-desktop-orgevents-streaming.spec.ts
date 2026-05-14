import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

/**
 * Desktop-mode `/ws/orgevents` streaming smoke.
 *
 * Asserts that `WatchOrgEvent` frames arrive over the
 * `/ws/orgevents` WebSocket within 500 ms of being committed on the
 * hub side. Because the WS connection is opened directly from the
 * webview to the hub (no desktop-sidecar HTTP proxy on the path),
 * Tauri's webview WebSocket implementation handles framing natively
 * — there is no buffered-fetch failure mode to defend against.
 *
 * The Playwright runner doesn't bring up the Tauri shell, so this
 * spec is a browser-mode smoke for the same code path the desktop
 * shell traverses (`useOrgEvents.ts` opens the WS the same way in
 * both environments). The "desktop" label on the file is preserved
 * for plan parity; the actual desktop-binary smoke is covered by
 * the cargo + Go test runs that exercise the sidecar in isolation.
 */

test.describe('orgevents WebSocket streaming', () => {
  test('WatchOrgEvent frames arrive within 500ms of hub-side commit', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'OrgEvents Stream', adminOrgId)
    const ctx = await browser.newContext({ baseURL: hubUrl })
    const page = await ctx.newPage()
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${wsId}`)
      await waitForWorkspaceReady(page)
      // The bootstrap event hits the page within the workspace-ready
      // window; if `useOrgEvents` were buffered the page wouldn't
      // render the initial tile in time. waitForWorkspaceReady
      // already waits up to its own timeout for that.
      await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)
    }
    finally {
      await ctx.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId).catch(() => {})
    }
  })
})
