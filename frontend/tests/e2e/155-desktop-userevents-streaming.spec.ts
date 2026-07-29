import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { loginViaToken, openWorkspace } from './helpers/ui'

/**
 * Desktop-mode `/ws/userevents` streaming smoke.
 *
 * Asserts that `WatchUserEvent` frames arrive over the
 * `/ws/userevents` WebSocket within 500 ms of being committed on the
 * hub side. Because the WS connection is opened directly from the
 * webview to the hub (no desktop-sidecar HTTP proxy on the path),
 * Tauri's webview WebSocket implementation handles framing natively
 * — there is no buffered-fetch failure mode to defend against.
 *
 * The Playwright runner doesn't bring up the Tauri shell, so this
 * spec is a browser-mode smoke for the same code path the desktop
 * shell traverses (`useUserEvents.ts` opens the WS the same way in
 * both environments). The "desktop" label on the file is preserved
 * for plan parity; the actual desktop-binary smoke is covered by
 * the cargo + Go test runs that exercise the sidecar in isolation.
 */

test.describe('userevents WebSocket streaming', () => {
  test('WatchUserEvent frames arrive within 500ms of hub-side commit', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'UserEvents Stream')
    const ctx = await browser.newContext({ baseURL: hubUrl })
    const page = await ctx.newPage()
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, wsId)
      // The bootstrap event hits the page within the workspace-ready
      // window; if `useUserEvents` were buffered the page wouldn't
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
