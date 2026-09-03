import type { Locator, Page } from '@playwright/test'
import path from 'node:path'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { workspaceRow } from './helpers/ui'

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
 * A second click on an already-open trigger CLOSES it, so the retry has to
 * check before it clicks -- the same shape `ensureExpanded` uses.
 */
async function openSectionMenu(page: Page, slug: string, anyItem: string | Locator): Promise<Locator> {
  const trigger = sectionMenuTrigger(page, slug)
  const popover = sectionMenuPopover(page, slug)
  const item = typeof anyItem === 'string'
    ? popover.getByRole('menuitem', { name: anyItem, exact: true })
    : anyItem
  await expect(async () => {
    if (!await item.isVisible())
      await trigger.click()
    await expect(item).toBeVisible()
  }).toPass()
  return popover
}

/**
 * Open a section's menu and click one of its items, retried TOGETHER.
 *
 * Same reasoning as `clickRowMenuItem` in `helpers/ui`: the sidebar re-renders
 * on workspace, worker and todo changes, so the menu can close between the open
 * and the click -- and a retry that covers only the open leaves the click
 * waiting on an item that will never be visible again.
 */
async function clickSectionMenuItem(page: Page, slug: string, itemName: string) {
  const trigger = sectionMenuTrigger(page, slug)
  const item = sectionMenuPopover(page, slug).getByRole('menuitem', { name: itemName, exact: true })
  await expect(async () => {
    if (!await item.isVisible())
      await trigger.click()
    await expect(item).toBeVisible()
    await item.click()
  }).toPass()
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

    // `workspaceRow` is `:visible`-scoped on purpose: the app mounts the
    // sidebar twice, so an unscoped lookup matches the off-screen copy too.
    await expect(workspaceItem).toHaveAttribute('data-expanded', 'false')

    await clickSectionMenuItem(page, 'workspaces_in_progress', 'Expand all')
    await expect(workspaceItem).toHaveAttribute('data-expanded', 'true')
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

    // Delete, through the confirm that states where the workspaces go.
    await clickSectionMenuItem(page, 'workspaces_custom', 'Delete section...')
    const confirm = page.locator('dialog:visible')
    await expect(confirm).toContainText('Its workspaces move to In progress')
    await confirm.getByRole('button', { name: 'Delete' }).click()
    await confirm.getByRole('button', { name: 'Confirm?' }).click()

    await expect(custom).toHaveCount(0)
  })
})
