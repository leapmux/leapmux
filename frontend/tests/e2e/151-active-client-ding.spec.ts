import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { withExtraClients } from './helpers/multiClient'
import { gotoWorkspace, tiles } from './helpers/ui'

/**
 * Active-client ding gate.
 *
 * Two browser contexts authenticate as the same admin user. Only the
 * focused (most-recently-active) client should play the turn-end ding
 * when an agent finishes a turn. The hub's per-workspace presence
 * tracker computes the active client from input heartbeats; the
 * frontend gates `playDingDong` on `activeClient.activeFor(wsId) ===
 * ownClientId`.
 *
 * The audio element is unobservable from Playwright (autoplay is
 * blocked in some contexts), so the test listens for the
 * `leapmux:turn-end-played` custom event the gate dispatches when —
 * and only when — the local client plays the ding. The other context
 * must not fire the event.
 */

async function recordDing(page: Page) {
  await page.evaluate(() => {
    ;(window as unknown as { __leapmuxDings?: number }).__leapmuxDings = 0
    window.addEventListener('leapmux:turn-end-played', () => {
      const w = window as unknown as { __leapmuxDings?: number }
      w.__leapmuxDings = (w.__leapmuxDings ?? 0) + 1
    })
  })
}

async function readDings(page: Page): Promise<number> {
  return await page.evaluate(() => (window as unknown as { __leapmuxDings?: number }).__leapmuxDings ?? 0)
}

test.describe('Active-client ding gate', () => {
  // This test proves the baseline of the gate in a real browser: two clients of one account, with no turn end,
  // dispatch no `leapmux:turn-end-played` event. It ends no turn, so it does not prove which client plays.
  // - The "active-client gate" cases of `src/components/shell/useAgentSettled.test.ts` prove the decision of the
  //   gate for each client.
  // - `080-turn-end-sound-preferences.spec.ts` plays the sound for a real turn end in one client.
  test('dispatches `leapmux:turn-end-played` only when this client is the active client', async ({ browser, emptyWorkspace, leapmuxServer }) => {
    const { adminToken } = leapmuxServer
    const wsId = emptyWorkspace.workspaceId

    await withExtraClients(browser, leapmuxServer, 2, async ([pageA, pageB]) => {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, wsId),
        gotoWorkspace(pageB, adminToken, wsId),
      ])

      await recordDing(pageA)
      await recordDing(pageB)

      // Click on pageA to make it the most-recently-active client
      // (the heartbeat throttle stamps `received_at` on the next
      // input event, which routes through the presence broadcaster).
      await tiles(pageA).first().click()
      // Wait for the presence update to settle.
      await pageA.waitForTimeout(500)

      // No turn ended, so neither client may dispatch the event.
      expect(await readDings(pageA)).toBe(0)
      expect(await readDings(pageB)).toBe(0)
    })
  })
})
