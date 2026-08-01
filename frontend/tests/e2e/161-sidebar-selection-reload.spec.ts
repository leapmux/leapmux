import type { Locator, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { openTerminalViaUI, sidebarLeaves, waitForWorkspaceReady } from './helpers/ui'

/**
 * The sidebar tab tree and the tab bar read DIFFERENT pointers -- the tree asks
 * `activeKeyForWorkspace`, the bar asks `activeKeyForTile` -- so a reload is the
 * one moment they can disagree, and nothing exercised it.
 *
 * They did disagree. `activeKeyForWorkspace` heals on read by synthesising
 * `mruHead` when nothing is chosen, `mru` is never persisted, and
 * `useTabPersistence` had no ordering guarantee against `restoreTabSelection`:
 * on the ticks where the writer ran first it persisted that synthesised
 * first-tab answer over the real stored key, and the restore then read back its
 * own clobbered value. The per-tile key was never synthesised, so the bar
 * stayed correct while the tree jumped to the first tab.
 *
 * Only a real reload reproduces it -- the unit tests in
 * `useTabPersistence.test.ts` pin the window itself, this pins the symptom.
 */

const TAB_COUNT = 3

/** Tab-bar tabs, scoped to the rendered copy: the shell mounts the shell twice. */
function tabs(page: Page): Locator {
  return page.locator('[data-testid="tab"]:visible')
}

/**
 * The sidebar tab-tree row the tree reports as active, scoped to THIS
 * workspace's subtree.
 *
 * `sidebarLeaves` carries the scoping this needs and a bare
 * `[data-testid="tab-tree-leaf"]` locator does not: the app mounts the sidebar
 * twice, and every EXPANDED workspace publishes its own `data-active` row, so
 * an app-wide locator can resolve to two nodes and fail on a strict-mode
 * violation that has nothing to do with the selection under test.
 */
function activeSidebarRow(page: Page, workspaceId: string): Locator {
  return sidebarLeaves(page, workspaceId).and(page.locator('[data-active="true"]'))
}

test.describe('sidebar selection across a reload', () => {
  test('the sidebar tree and the tab bar agree on the active tab', async ({ page, authenticatedWorkspace }) => {
    const wsId = authenticatedWorkspace.workspaceId
    await waitForWorkspaceReady(page)

    // A fixed number of opens, each awaited to completion, rather than looping
    // on the count: `openTerminalViaUI` returns when the RPC resolves, which is
    // BEFORE the CRDT op projects the tab, so a count-driven loop reads a stale
    // total and clicks again -- landing four tabs and failing its own
    // assertion.
    const startingTabs = await tabs(page).count()
    for (let i = startingTabs; i < TAB_COUNT; i++) {
      await openTerminalViaUI(page)
      await expect(tabs(page)).toHaveCount(i + 1)
    }
    await expect(tabs(page)).toHaveCount(TAB_COUNT)

    // Activate the MIDDLE tab: first and last are what a broken fallback picks.
    await tabs(page).nth(1).click()
    await expect(tabs(page).nth(1)).toHaveAttribute('aria-selected', 'true')

    // Identify the tab by id, not by its rendered label. Two terminals can
    // carry the same title -- the shell's OSC title wins, and two shells in one
    // cwd report the same one -- so a text assertion would pass even if the
    // tree landed on the wrong terminal.
    const activeTabId = await tabs(page).nth(1).getAttribute('data-tab-id')
    expect(activeTabId).toBeTruthy()
    await expect(activeSidebarRow(page, wsId)).toHaveAttribute('data-tab-id', activeTabId!)

    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(tabs(page)).toHaveCount(TAB_COUNT)

    // The tab bar reads the per-tile pointer and always survived the reload.
    await expect(tabs(page).nth(1)).toHaveAttribute('aria-selected', 'true')
    await expect(tabs(page).nth(1)).toHaveAttribute('data-tab-id', activeTabId!)

    // The tree reads the per-workspace pointer, which is what the writer used
    // to overwrite with the first tab.
    await expect(activeSidebarRow(page, wsId)).toHaveCount(1)
    await expect(activeSidebarRow(page, wsId)).toHaveAttribute('data-tab-id', activeTabId!)
  })
})
