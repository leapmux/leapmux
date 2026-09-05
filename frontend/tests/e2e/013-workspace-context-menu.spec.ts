import type { Locator } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { COARSE_POINTER_METRICS, touchDown } from './helpers/touch'
import { loginViaToken, openWorkspace, workspaceRow } from './helpers/ui'
import { createGitRepo, createWorkspaceWithWorktreeViaAPI } from './helpers/worktree'

/**
 * The workspace row menu carries an INFO BLOCK and, once the workspace spans
 * more than one repository, a row named after each repository. Playwright
 * matches an accessible name by SUBSTRING unless told otherwise -- so
 * `{ name: 'Delete' }` also matched the info block, whose name is every row of
 * it joined, and a repository named after a branch matched the branch items.
 * Every lookup below is `exact`.
 */

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
    await page.getByRole('menuitem', { name: 'Rename', exact: true }).click()

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
    await page.getByRole('menuitem', { name: 'Delete', exact: true }).click()

    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete' }).click()
    await dialog.getByRole('button', { name: 'Confirm?' }).click()

    await expect(workspaceItem).not.toBeVisible()
  })

  /**
   * The menu asks WHICH REPOSITORY first, and hangs every action on it.
   *
   * One repository renders flat, because a submenu holding the only choice is
   * a click nobody should have to make -- and the two start items then name
   * the repository themselves, which is why no header sits above them.
   *
   * A browser session has no local Worker, so `Reveal in file manager` and the
   * `Open in ...` rows are hidden by design. The two clipboard items are what
   * a browser really shows.
   */
  test('one repository renders its start items and its repository block flat', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'ws-menu-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'WS Menu Repo',
      repoDir,
      'ws-menu-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const row = workspaceRow(page, workspaceId)
    await row.hover()
    await row.locator('[data-testid="workspace-row-menu-trigger"]').click()

    const menu = page.locator('menu[popover]:visible')
    // Flat: no `Repositories` header, and no submenu to walk into.
    await expect(menu.getByText('Repositories', { exact: true })).toHaveCount(0)

    // The start items name the repository, which is today's label unchanged.
    await expect(menu.getByRole('menuitem', { name: /^New agent in .+\.\.\.$/ })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: /^New terminal in .+\.\.\.$/ })).toBeVisible()
    // ...so the no-target pair, which means "follow the current tab context",
    // must not also appear. A workspace that HAS a repository never falls back
    // to it: that would start an agent on a machine the user never picked.
    await expect(menu.getByRole('menuitem', { name: 'New agent...', exact: true })).toHaveCount(0)
    await expect(menu.getByRole('menuitem', { name: 'New terminal...', exact: true })).toHaveCount(0)

    // The shared repository block, the same one the branch and repository rows
    // render.
    await expect(menu.getByText('Repository', { exact: true })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Copy repository path', exact: true })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Reveal in file manager', exact: true })).toHaveCount(0)

    // Everything below the repository block is untouched by the restructure.
    await expect(menu.getByRole('menuitem', { name: 'Rename', exact: true })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Archive', exact: true })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Delete', exact: true })).toBeVisible()
  })

  /**
   * A workspace with no checkout at all is the row that most needs a way in,
   * so the two items still open with no target -- meaning "follow the current
   * tab context" -- and no repository block is rendered above them.
   *
   * A workspace of its OWN, not the shared fixture's: that one holds an agent
   * in a repository, so its menu names the repository instead. Only a
   * workspace with no tab has no checkout.
   */
  test('a workspace with no repository keeps the no-target start items', async ({
    page,
    authenticatedWorkspace,
    leapmuxServer,
  }) => {
    const emptyId = await createWorkspaceViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, 'No Repo WS')
    try {
      // Both rows are on screen, and the menu below belongs to the empty one.
      // The fixture's workspace holds a repository, so its own menu names it --
      // which is what made this row necessary.
      await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()
      const row = workspaceRow(page, emptyId)
      await expect(row).toBeVisible()
      await row.hover()
      await row.locator('[data-testid="workspace-row-menu-trigger"]').click()

      const menu = page.locator('menu[popover]:visible')
      await expect(menu.getByRole('menuitem', { name: 'New agent...', exact: true })).toBeVisible()
      await expect(menu.getByRole('menuitem', { name: 'New terminal...', exact: true })).toBeVisible()
      await expect(menu.getByText('Repository', { exact: true })).toHaveCount(0)
      await expect(menu.getByText('Repositories', { exact: true })).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, emptyId).catch(() => {})
    }
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

    // Scoped to THIS row: the popover belongs to the row that owns it, and
    // every row on screen carries one.
    const renameItem = workspaceItem.getByRole('menuitem', { name: 'Rename', exact: true })
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

      // Scoped to THIS row, not to the page. The menu's popover is a DOM child
      // of the row that owns it, and every other row on screen has one too --
      // so a page-global lookup answers for whichever row happens to have a
      // menu open, which is not what either assertion below means.
      const renameItem = workspaceItem.getByRole('menuitem', { name: 'Rename', exact: true })

      // ── A press that travels is a scroll, not a menu ────────────────────────
      const scrolling = await touchDown(page, x, y)
      await scrolling.moveTo(x, y + 60)
      await scrolling.end()
      await expect(renameItem).toBeHidden()
      await expect(workspaceItem).not.toHaveAttribute('data-press-hold')

      // ── A press that stays put opens the menu, while it is still down ───────
      const holding = await touchDown(page, x, y)
      // The indicator is armed while the finger is still down, which is the only
      // thing telling the user a menu is coming.
      await expect(workspaceItem).toHaveAttribute('data-press-hold', '')

      // The menu is the oracle for the hold completing: it opens ON the hold,
      // with the finger still on the glass. (This used to wait for the accent
      // tint to reach full instead, which is no longer a state that lasts --
      // the tint yields the moment the menu it was promising arrives.)
      await expect(renameItem).toBeVisible()
      await expect(workspaceItem).not.toHaveAttribute('data-press-hold')

      // The lift leaves the menu up. A `popover="auto"` shown under a finger is
      // what the HTML light-dismiss pass acts on, so this pins the repair that
      // keeps it -- and keeps it without a blink, since the menu never closes.
      await holding.end()
      await expect(renameItem).toBeVisible()
      // The press must not also fire the row's own click.
      expect(await workspaceItem.getAttribute('data-active')).toBe(before)

      // A press-opened menu is `popover="manual"`, so the platform's own light
      // dismiss never touches it -- which is exactly why the release cannot take
      // it either. Closing on a press outside is the job that comes with that,
      // and this is the assertion that it was actually done.
      await page.locator('body').dispatchEvent('pointerdown')
      await expect(renameItem).toBeHidden()
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
