import { expect, test } from './fixtures'
import { tabbarAgentLabels } from './helpers/ui'

/**
 * Regression: closing one of two SPLIT children used to leave the
 * SPLIT alive with one live cell. The projection's single-child SPLIT
 * collapse rule re-keyed the rendered leaf to the parent SPLIT's
 * node_id, but the surviving tab's stored `tile_id` still pointed at
 * the actual child node — so `getTabsForTile(rendered tile id)`
 * returned [] and the user saw an empty tile, even though the sidebar
 * still listed the tab.
 *
 * Fix: `emitCloseTile` detects "parent SPLIT will be left with one
 * live child" and emits an inverse-split batch: tombstone the closing
 * tile + sibling, migrate sibling tabs to the parent, flip the
 * parent's NodeKind back to LEAF in place. The unit tests in
 * `src/stores/layout.store.crdt.test.ts` pin the op shape;
 * this spec exercises the user-visible behaviour through the actual
 * Tile close button so the full chain (UI handler → emitCloseTile →
 * hub broadcast → reconciler → render) is covered.
 *
 * Adds a `page.reload()` checkpoint per the "drive layout via UI +
 * reload after every CRDT-state mutation" pattern: the bug presented
 * locally even when the CRDT batch had been accepted by the hub
 * (the projection's collapse rule was independent of the kind-flip
 * commit), so a regression that lost the kind-flip on the wire would
 * pass the in-session assertions and only fail after refresh.
 */

test.describe('Tile close undo-split', () => {
  test('closing the empty sibling leaves the surviving tab visible on the merged leaf; survives reload', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // Fixture seeds one agent. Capture its title so we can assert the
    // tab survives the undo-split path with full metadata intact.
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)
    const initialLabels = await tabbarAgentLabels(page)
    expect(initialLabels).toHaveLength(1)
    const seededTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]')
      .first()
      .getAttribute('data-tab-id')
    expect(seededTabId).toBeTruthy()

    // Split horizontally — the new right-side tile is empty, and the
    // seeded agent stays on the left. Both tiles render close buttons.
    await page.locator('[data-testid="split-horizontal"]').first().click()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(2)

    // Close the empty (right) tile. Both children show a `close-tile`
    // button on a 2-child SPLIT; the second one (index 1) belongs to
    // the right child. The empty tile takes the dialog-less path —
    // `closeTileFlow.request` short-circuits to `finalize()` directly
    // when the closeable holds zero tabs (see `closeFlow.ts`).
    await page.locator('[data-testid="close-tile"]').nth(1).click()

    // Projection: collapse to a single leaf, original agent visible.
    // Pre-fix this passed shallowly — `tile` count became 1 — but the
    // tab's stored tileId pointed at the now-tombstoned right child's
    // sibling-id, so the tabbar rendered empty. The post-fix path
    // migrates the tab's tile_id to the parent SPLIT (which flips to
    // LEAF), and the tab is rendered on the merged tile.
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    const afterCloseLabels = await tabbarAgentLabels(page)
    expect(afterCloseLabels).toEqual(initialLabels)
    const survivingTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]')
      .first()
      .getAttribute('data-tab-id')
    expect(survivingTabId).toBe(seededTabId)

    // Reload checkpoint — the undo-split batch flipped the parent's
    // NodeKind back to LEAF. If that op were dropped on the wire (or
    // rolled back by a hub-side guard), the in-session render could
    // still appear correct via the local layoutStore but the refresh
    // would re-derive a stale SPLIT-with-one-child tree from the
    // hub's confirmed state.
    await page.reload()
    await page.locator('[data-testid="tile"]').first().waitFor()
    await expect(page.locator('[data-testid="tile"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    const reloadLabels = await tabbarAgentLabels(page)
    expect(reloadLabels).toEqual(initialLabels)
    const reloadTabId = await page.locator('[data-testid="tab"][data-tab-type="agent"]')
      .first()
      .getAttribute('data-tab-id')
    expect(reloadTabId).toBe(seededTabId)
  })
})
