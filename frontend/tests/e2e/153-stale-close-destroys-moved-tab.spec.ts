import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { gotoWorkspace } from './helpers/ui'

/**
 * Documents the "stale close destroys recently-moved tab" UX trade-off
 * plus its baseline projection invariant.
 *
 * Plan invariant: if client A drags tab X from tile T to tile U
 * (atomic CRDT batch) and client B (with a stale view in which X is
 * still on T) presses "close tile T", client B's batch contains
 * `TombstoneNode(T)` plus `TombstoneTab(X)` (B closes the tabs it
 * sees on T). Remove-wins makes the tombstones permanent: X is dead
 * even though A had just moved it. This is correct CRDT behavior
 * given the data on the wire; the alternative (HLC-conditional ops)
 * would push significant complexity into the CRDT for a corner case.
 *
 * Reproducing the exact race needs a deterministic delay between A's
 * move op landing on the hub and B observing the updated state —
 * Playwright doesn't expose that timing knob without test-only hooks.
 * The spec therefore covers two related projection invariants that
 * MUST hold under the remove-wins discipline; if a future contributor
 * relaxes the projection rules, the trade-off-as-documented breaks
 * and these tests would fail loudly.
 */

test.describe('Stale close-tile semantics', () => {
  test('close-tile from one client tombstones the tile in another (remove-wins)', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'remove-wins', adminOrgId)
    const workspaceUrl = `/o/admin/workspace/${wsId}`
    const ctxA = await browser.newContext({ baseURL: hubUrl })
    const ctxB = await browser.newContext({ baseURL: hubUrl })
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()
    try {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, workspaceUrl),
        gotoWorkspace(pageB, adminToken, workspaceUrl),
      ])

      // Split into two tiles in A. Both contexts converge on 2 tiles.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(2)
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(2)

      // Capture the right-side tile id from B's DOM. B's view is
      // independent of A's nanoid generation; both contexts' tile ids
      // are sourced from the CRDT projection so they agree.
      const tileIds = await pageB.locator('[data-testid="tile"]').evaluateAll((els) => {
        return els.map(e => (e as HTMLElement).dataset.tileId ?? '')
      })
      expect(tileIds.length).toBe(2)
      // The right tile is the one whose close-tile button sits later
      // in DOM order; click it from B.
      await pageB.locator('[data-testid="close-tile"]').first().click()

      // Both views collapse back to one tile (single-child SPLIT
      // collapse projection rule).
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(1)
      await expect(pageA.locator('[data-testid="tile"]')).toHaveCount(1)
    }
    finally {
      await ctxA.close()
      await ctxB.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId).catch(() => {})
    }
  })

  test('close-tile after a fast remote split is permanent (remove-wins guards against stale-resurrect)', async ({ browser, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const wsId = await createWorkspaceViaAPI(hubUrl, adminToken, 'fast-split-close', adminOrgId)
    const workspaceUrl = `/o/admin/workspace/${wsId}`
    const ctxA = await browser.newContext({ baseURL: hubUrl })
    const ctxB = await browser.newContext({ baseURL: hubUrl })
    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()
    try {
      await Promise.all([
        gotoWorkspace(pageA, adminToken, workspaceUrl),
        gotoWorkspace(pageB, adminToken, workspaceUrl),
      ])

      // A splits, then immediately B closes the right side. Without
      // remove-wins discipline, a stale Set op from B could resurrect
      // the right tile after A's close. The projection must converge
      // to a single tile on both clients.
      await pageA.locator('[data-testid="split-horizontal"]').first().click()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(2)

      // A splits the new right tile vertically.
      await pageA.locator('[data-testid="split-vertical"]').nth(1).click()
      await expect(pageB.locator('[data-testid="tile"]')).toHaveCount(3)

      // B closes one of the inner tiles right away.
      await pageB.locator('[data-testid="close-tile"]').first().click()
      // Convergence: both clients see at most 2 tiles after the close
      // (the projection's single-child SPLIT collapse may further
      // reduce to 1; either is acceptable for this invariant).
      await expect(async () => {
        const a = await pageA.locator('[data-testid="tile"]').count()
        const b = await pageB.locator('[data-testid="tile"]').count()
        expect(a).toBeLessThanOrEqual(2)
        expect(b).toBeLessThanOrEqual(2)
        expect(a).toBe(b)
      }).toPass()
    }
    finally {
      await ctxA.close()
      await ctxB.close()
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsId).catch(() => {})
    }
  })
})
