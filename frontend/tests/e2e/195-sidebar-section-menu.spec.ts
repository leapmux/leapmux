import type { Locator, Page } from '@playwright/test'
import path from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { clickRowMenuItem, openRowMenu, workspaceRow } from './helpers/ui'

/**
 * The section header's menu, which replaced the `+`.
 *
 * The item SET is unit-tested in `WorkspaceSectionMenu.test.tsx`. What needs a
 * real browser is what jsdom cannot answer: that the trigger renders while the
 * section is COLLAPSED (the archived section ships closed, and its menu is the
 * only route to the bulk operations), that a repository row really pre-fills
 * the dialog against a live worker, and that Collapse all touches one section's
 * rows and no other's.
 *
 * Every `getByRole('menuitem')` below is `exact`. Playwright matches an
 * accessible name by SUBSTRING otherwise, and this menu holds items whose names
 * contain one another ("Unarchive all" inside "Archive", "New workspace..."
 * beside a repository row).
 */

const frontendDir = path.resolve(import.meta.dirname, '../..')

function sectionMenuTrigger(page: Page, slug: string): Locator {
  return page.locator(`[data-testid="sidebar-section-menu-${slug}"]:visible`).first()
}

function sectionMenuPopover(page: Page, slug: string): Locator {
  return page.locator(`[data-testid="sidebar-section-menu-${slug}-popover"]`)
}

function sectionHeader(page: Page, slug: string): Locator {
  return page.locator(`[data-testid="section-header-${slug}"]:visible`).first()
}

/**
 * Open one section's menu, idempotently.
 *
 * Through the shared `openRowMenu`, which already retries the open as one unit
 * because the sidebar re-renders on workspace, worker and todo changes. It
 * takes `null` for the row: a section header's trigger is always painted, so
 * there is nothing to hover first.
 */
async function openSectionMenu(page: Page, slug: string, anyItem: string | Locator): Promise<Locator> {
  const popover = sectionMenuPopover(page, slug)
  const item = typeof anyItem === 'string'
    ? popover.getByRole('menuitem', { name: anyItem, exact: true })
    : anyItem
  await openRowMenu(null, sectionMenuTrigger(page, slug), item)
  return popover
}

/** Open a section's menu and click one of its items, retried TOGETHER. */
async function clickSectionMenuItem(page: Page, slug: string, itemName: string) {
  const item = sectionMenuPopover(page, slug).getByRole('menuitem', { name: itemName, exact: true })
  await clickRowMenuItem(null, sectionMenuTrigger(page, slug), item)
}

/** Archive the fixture's workspace through its row menu. */
async function archiveWorkspace(page: Page, workspaceId: string) {
  const row = workspaceRow(page, workspaceId)
  await row.hover()
  await row.locator('button').first().click()
  await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
  await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()
  await expect(sectionHeader(page, 'workspaces_archived')).toBeVisible()
}

test.describe('sidebar section menu', () => {
  test('a repository row pre-fills the New workspace dialog', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    // A second agent in this repository's own checkout, so the section's tabs
    // carry a git toplevel the menu can offer.
    await openAgentViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId, frontendDir)
    await page.reload()
    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()

    // Addressed by its test id, not by a name: the row's LABEL is the formatted
    // origin URL for a repository that has one, which this spec would otherwise
    // have to predict.
    const repoRow = sectionMenuPopover(page, 'workspaces_in_progress')
      .getByTestId('sidebar-new-workspace-repo')
      .first()
    // Open and click as one retried unit -- the repository group appears only
    // once the tab's git state lands, so the first opens can find no row.
    await expect(async () => {
      await openSectionMenu(page, 'workspaces_in_progress', repoRow)
      await repoRow.click()
    }).toPass()

    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()
    // The worker the repository is on, not "pick one".
    await expect(page.getByTestId('worker-select-menu-trigger')).toContainText('Local')
    // The seeded path-info snapshot means the git options paint on the FIRST
    // render rather than after a probe round trip.
    await expect(page.getByLabel('Use current state')).toBeVisible()
  })

  test('the archived section menu opens while the section is COLLAPSED', async ({ page, authenticatedWorkspace }) => {
    await archiveWorkspace(page, authenticatedWorkspace.workspaceId)

    // Archiving auto-expands the section; collapse it again so the menu is
    // asked for from the state the section actually ships in.
    await sectionHeader(page, 'workspaces_archived').click()

    const popover = await openSectionMenu(page, 'workspaces_archived', 'Unarchive all')
    await expect(popover.getByRole('menuitem', { name: 'Empty archive...', exact: true })).toBeVisible()
    // No create items: a workspace created into Archived is born read-only.
    await expect(popover.getByRole('menuitem', { name: 'New workspace...', exact: true })).toHaveCount(0)
  })

  // The filter box lives in the section BODY, and this menu opens while the
  // section is collapsed -- where a hidden input takes no focus and the
  // checkbox would report itself checked for a control nobody can see. The
  // section def expands first, the same rule "Reveal active workspace"
  // follows. jsdom cannot show this: a collapsed body is `visibility: hidden`
  // inside a zero-height row, which only a real layout produces.
  test('Filter workspaces expands the section it belongs to', async ({ page, authenticatedWorkspace }) => {
    await archiveWorkspace(page, authenticatedWorkspace.workspaceId)
    // Archiving auto-expands the section; collapse it again. `data-closed` is
    // the section's own state -- a collapsed BODY still reports a box, so the
    // rows inside it are not a usable signal here.
    const section = sectionHeader(page, 'workspaces_archived')
    // The SUMMARY row is the toggle; the pane around it is not.
    await page.locator('[data-testid="section-header-workspaces_archived-summary"]:visible').first().click()
    await expect(section).toHaveAttribute('data-closed', '')

    // By its test id, not by role: this row is a `menuitemcheckbox`, so the
    // `menuitem` lookup the other items use never matches it.
    const toggle = sectionMenuPopover(page, 'workspaces_archived').getByTestId('sidebar-filter-workspaces')
    await clickRowMenuItem(null, sectionMenuTrigger(page, 'workspaces_archived'), toggle)

    // The section opened, and the box it opened for takes text. Without the
    // expand-first rule the input mounts inside a `visibility: hidden` body,
    // so it is never `:visible` and never focusable.
    await expect(section).not.toHaveAttribute('data-closed', '')
    const filter = page.locator('[data-testid^="workspace-filter-"]:visible').first()
    await expect(filter).toBeVisible()
    await filter.fill('no-such-workspace')
    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toHaveCount(0)
  })

  test('Unarchive all empties the archive back into In progress', async ({ page, authenticatedWorkspace }) => {
    await archiveWorkspace(page, authenticatedWorkspace.workspaceId)

    await clickSectionMenuItem(page, 'workspaces_archived', 'Unarchive all')

    // The row is back, and the archive offers no bulk operations any more.
    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()
    const reopened = await openSectionMenu(page, 'workspaces_archived', 'New section...')
    await expect(reopened.getByRole('menuitem', { name: 'Unarchive all', exact: true })).toHaveCount(0)
  })

  test('Collapse all collapses the rows of THAT section', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)
    // The active workspace auto-expands once it has a tab.
    await expect(workspaceItem).toHaveAttribute('data-expanded', 'true')

    await clickSectionMenuItem(page, 'workspaces_in_progress', 'Collapse all')

    // `workspaceRow` is `:visible`-scoped on purpose: a sidebar collapsed to
    // its rail keeps its rows mounted under `display: none`, so an unscoped
    // lookup can match a row nobody can see.
    await expect(workspaceItem).toHaveAttribute('data-expanded', 'false')

    await clickSectionMenuItem(page, 'workspaces_in_progress', 'Expand all')
    await expect(workspaceItem).toHaveAttribute('data-expanded', 'true')
  })

  // The isolation the header comment promises, which the single-section case
  // above cannot show: "Collapse all" is a SET operation over one section's
  // ids, and a regression to writing the whole set back would still pass a
  // test that owns every row on screen.
  test('Collapse all leaves ANOTHER section\'s rows expanded', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const second = await createWorkspaceViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, 'second-ws')
    await openAgentViaAPI(
      leapmuxServer.hubUrl,
      leapmuxServer.adminToken,
      leapmuxServer.workerId,
      second,
      frontendDir,
    )
    await page.reload()

    const first = workspaceRow(page, authenticatedWorkspace.workspaceId)
    const other = workspaceRow(page, second)
    await expect(other).toBeVisible()

    // Both expanded, both in In progress; then archive the second so the two
    // sit in DIFFERENT sections.
    await archiveWorkspace(page, second)
    await expect(sectionHeader(page, 'workspaces_archived')).toBeVisible()
    await page.locator(`[data-testid="workspace-chevron-${second}"]:visible`).first().click()
    await expect(other).toHaveAttribute('data-expanded', 'true')
    await expect(first).toHaveAttribute('data-expanded', 'true')

    await clickSectionMenuItem(page, 'workspaces_in_progress', 'Collapse all')

    await expect(first).toHaveAttribute('data-expanded', 'false')
    await expect(other).toHaveAttribute('data-expanded', 'true')
  })

  test('a custom section can be created, renamed and deleted from the menu', async ({ page, authenticatedWorkspace }) => {
    await expect(workspaceRow(page, authenticatedWorkspace.workspaceId)).toBeVisible()

    await clickSectionMenuItem(page, 'workspaces_in_progress', 'New section...')

    const dialog = page.locator('[data-testid="section-name-dialog"]')
    await expect(dialog).toBeVisible()
    await dialog.getByTestId('title-input').fill('Reviews')
    await dialog.getByRole('button', { name: 'Create' }).click()

    const custom = page.locator('[data-testid="section-header-workspaces_custom"]:visible').first()
    await expect(custom).toBeVisible()
    await expect(custom).toContainText('Reviews')

    // Rename. Only a CUSTOM section offers this -- the hub refuses a built-in.
    await clickSectionMenuItem(page, 'workspaces_custom', 'Rename section...')
    await expect(dialog).toBeVisible()
    await dialog.getByTestId('title-input').fill('Code review')
    await dialog.getByRole('button', { name: 'Rename' }).click()
    await expect(custom).toContainText('Code review')

    // With a second workspace section on screen, the row menu offers Move to --
    // and this is the ONLY place in the suite that opens a `SubMenu` in a real
    // browser. Every item behind one (Move to, Repository, New agent in <repo>)
    // is otherwise covered only under vitest, where `showPopover`/`hidePopover`
    // are stubbed, so a nested popover that never opens would pass everything.
    const row = workspaceRow(page, authenticatedWorkspace.workspaceId)
    await clickRowMenuItem(
      row,
      row.locator('button').first(),
      page.getByRole('menuitem', { name: 'Move to', exact: true }),
    )
    const moveTo = page.locator('[data-testid="workspace-move-to-popover"]')
    await expect(moveTo.getByRole('menuitem', { name: 'Code review', exact: true })).toBeVisible()
    // `isMoveTargetSection` keeps a workspace out of a section it cannot live
    // in, which is what 014 claimed to cover and could not.
    await expect(moveTo.getByRole('menuitem', { name: 'Files', exact: true })).toHaveCount(0)
    await expect(moveTo.getByRole('menuitem', { name: 'To-dos', exact: true })).toHaveCount(0)

    await moveTo.getByRole('menuitem', { name: 'Code review', exact: true }).click()
    await expect(custom.locator('..').locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`))
      .toHaveCount(1)

    // Delete, through the confirm that states where the workspaces go.
    await clickSectionMenuItem(page, 'workspaces_custom', 'Delete section...')
    const confirm = page.locator('dialog:visible')
    await expect(confirm).toContainText('Its workspaces move to In progress')
    await confirm.getByRole('button', { name: 'Delete' }).click()
    await confirm.getByRole('button', { name: 'Confirm?' }).click()

    await expect(custom).toHaveCount(0)
  })
})
