import type { Locator } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { COARSE_POINTER_METRICS, touchDown } from './helpers/touch'
import { workspaceRow } from './helpers/ui'

/**
 * The context menu's item visibility (Rename / Share / Archive vs. Unarchive /
 * Delete / Move-to) and the owner-only filter are unit-tested in
 * `src/components/workspace/WorkspaceContextMenu.test.tsx`. This e2e exercises
 * the parts that need a real session + backend round-trip: inline rename
 * persisting via UpdateWorkspace, and the two-step Delete dialog actually
 * removing the workspace from the sidebar after the server processes it.
 */
test.describe('Workspace Context Menu', () => {
  test('rename via context menu and delete via two-step confirm round-trip the backend', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)
    await expect(workspaceItem).toBeVisible()

    // ── Rename ──────────────────────────────────────────────────────────────
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    const renameInput = workspaceItem.locator('input')
    await expect(renameInput).toBeVisible()
    await expect(renameInput).toBeFocused()
    await renameInput.fill('Renamed Workspace')
    await renameInput.press('Enter')

    await expect(renameInput).not.toBeVisible()
    await expect(page.getByText('Renamed Workspace')).toBeVisible()

    // ── Delete (two-step) ───────────────────────────────────────────────────
    // Navigate to the dashboard so the workspace can be safely deleted.
    await page.goto('/')
    await expect(workspaceItem).toBeVisible()

    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()

    await expect(workspaceItem).not.toBeVisible()
  })

  /**
   * The two inputs that reach the menu without a hover. The item set itself is
   * covered above and in the unit tests; what needs a real browser here is the
   * platform behaviour the gesture works around -- popover light dismiss on the
   * pointer release, and the row's own click.
   */
  test('right-click opens the menu at the cursor without selecting the row', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)
    await expect(workspaceItem).toBeVisible()

    const before = await workspaceItem.getAttribute('data-active')
    const box = (await workspaceItem.boundingBox())!
    const x = box.x + box.width / 2
    const y = box.y + box.height / 2

    await page.mouse.move(x, y)
    await page.mouse.down({ button: 'right' })
    await page.mouse.up({ button: 'right' })

    const renameItem = page.getByRole('menuitem', { name: 'Rename' })
    await expect(renameItem).toBeVisible()

    // Still visible a moment later: the menu opens after the release, so the
    // release's own light-dismiss pass cannot take it back down again.
    await expect(renameItem).toBeVisible()

    // The menu lands ON the cursor, not at the row's edge.
    const menuBox = (await page.locator('menu:popover-open').boundingBox())!
    expect(Math.abs(menuBox.x - x)).toBeLessThan(4)

    // A secondary click reads the row; it must not select it.
    expect(await workspaceItem.getAttribute('data-active')).toBe(before)
  })

  test.describe('touch long press', () => {
    // A desktop viewport keeps the sidebar docked, which keeps `workspaceRow`
    // simple. `hasTouch` alone is enough here because the gesture branches on
    // `pointerType` in JS, not on a `(pointer: coarse)` media query.
    test.use({ hasTouch: true })

    test('a long press opens the menu; a press that scrolls away does not', async ({ page, authenticatedWorkspace }) => {
      const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)
      await expect(workspaceItem).toBeVisible()

      const before = await workspaceItem.getAttribute('data-active')
      const box = (await workspaceItem.boundingBox())!
      const x = box.x + box.width / 2
      const y = box.y + box.height / 2

      // ── A press that travels is a scroll, not a menu ────────────────────────
      const scrolling = await touchDown(page, x, y)
      await scrolling.moveTo(x, y + 60)
      await scrolling.end()
      await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeHidden()
      await expect(workspaceItem).not.toHaveAttribute('data-press-hold')

      // ── A press that stays put opens the menu ───────────────────────────────
      const holding = await touchDown(page, x, y)
      // The indicator is armed while the finger is still down, which is the only
      // thing telling the user a menu is coming.
      await expect(workspaceItem).toHaveAttribute('data-press-hold', '')

      // Hold until the accent tint reaches full. That is the hold completing --
      // the CSS ramp and the JS timer are the same `motion.longPress` -- so this
      // waits on the real threshold instead of sleeping a guessed interval, and
      // proves the indicator the user watches actually runs.
      await expect.poll(async () => Number.parseFloat(
        await workspaceItem.evaluate(el => getComputedStyle(el, '::before').opacity),
      )).toBeGreaterThan(0.99)

      await holding.end()

      await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeVisible()
      await expect(workspaceItem).not.toHaveAttribute('data-press-hold')
      // The press must not also fire the row's own click.
      expect(await workspaceItem.getAttribute('data-active')).toBe(before)
    })
  })

  test.describe('coarse pointer', () => {
    // The kebab reveal is a `(pointer: coarse)` media rule, and Blink derives the
    // primary pointer type from the MOBILE VIEWPORT, not from `hasTouch` -- so the
    // full device metrics are what make this test test anything.
    test.use(COARSE_POINTER_METRICS)

    test('only the selected row shows its kebab', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
      // A SECOND workspace, so the "not selected" half of the rule has something
      // to be true about. With one row the comparison would be vacuous.
      const otherId = await createWorkspaceViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, 'Unselected')
      try {
        // The mobile layout keeps the sidebar in a drawer.
        await page.getByRole('button', { name: 'Toggle workspaces' }).click()

        const selected = workspaceRow(page, authenticatedWorkspace.workspaceId)
        const unselected = workspaceRow(page, otherId)
        await expect(selected).toBeVisible()
        await expect(unselected).toBeVisible()
        await expect(selected).toHaveAttribute('data-active', 'true')
        await expect(unselected).toHaveAttribute('data-active', 'false')

        const kebabOpacity = (row: Locator) =>
          row.locator('[aria-expanded]').first().evaluate(el => Number.parseFloat(getComputedStyle(el).opacity))

        // Painted, not merely present: the trigger has always been hit-testable at
        // `opacity: 0`, so only the computed value proves the rule fired.
        await expect.poll(() => kebabOpacity(selected)).toBe(1)
        // And the other row stays clean, which is the whole point of selecting one.
        expect(await kebabOpacity(unselected)).toBe(0)
      }
      finally {
        await deleteWorkspaceViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, otherId).catch(() => {})
      }
    })
  })
})
