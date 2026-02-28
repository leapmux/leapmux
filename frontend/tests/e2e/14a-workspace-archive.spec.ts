import process from 'node:process'
import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  loginViaToken,
  openWorkspaceContextMenu,
  waitForWorkspaceReady,
} from './helpers'

test.describe('Workspace Archive', () => {
  test('should archive workspace via context menu with confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu and click Archive (top-level menu item)
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Archive' }).click()

    // Confirmation dialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Archive Workspace')).toBeVisible()
    await expect(dialog.getByText('All active agents and terminals will be stopped')).toBeVisible()

    // Confirm the archive
    await dialog.getByRole('button', { name: 'Archive' }).click()

    // Workspace should now be in the archived section (auto-expanded)
    const archivedSection = page.locator('[data-testid="section-header-workspaces_archived"]')
    await expect(archivedSection).toBeVisible()

    // Workspace item should be visible inside the archived section (auto-expanded)
    await expect(workspaceItem).toBeVisible()
  })

  test('should cancel archive via confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu and click Archive (top-level menu item)
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Archive' }).click()

    // Confirmation dialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()

    // Cancel the archive
    await dialog.getByRole('button', { name: 'Cancel' }).click()

    // Dialog should close and workspace should still be in its original section
    await expect(dialog).not.toBeVisible()
    await expect(workspaceItem).toBeVisible()
  })

  test('should unarchive workspace and restore normal behavior', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)

    // Archive the workspace first: open context menu using the workspace item directly
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // Wait for the archived section to appear (auto-expanded)
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()
    await expect(workspaceItem).toBeVisible()

    // Now unarchive it: open context menu using the workspace item directly
    await workspaceItem.hover()
    await workspaceItem.locator('button').first().click()
    await page.getByRole('menuitem', { name: 'Unarchive' }).click()

    // Workspace is active again — add-tab buttons should be visible
    await expect(page.locator('[data-testid="new-agent-button"]')).toBeVisible()
  })

  test('should not show Move-to when workspace is in the only target section', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu — with only one workspace section (In Progress),
    // "Move to" should not appear since there are no other target sections
    await openWorkspaceContextMenu(page, workspaceName!.trim())

    // "Move to" should not be visible (no other non-archived, non-shared sections to move to)
    await expect(page.getByRole('menuitem', { name: 'Move to' })).not.toBeVisible()

    // Other menu items should be present
    await expect(page.getByRole('menuitem', { name: 'Rename' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Archive' })).toBeVisible()
  })

  test('should only show valid workspace sections in Move-to menu', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Open context menu
    await openWorkspaceContextMenu(page, workspaceName!.trim())

    // Shared, Files, To-dos should NOT appear as menu items for move targets
    const menuItems = page.getByRole('menuitem')
    const allLabels = await menuItems.allTextContents()
    expect(allLabels).not.toContain('Shared')
    expect(allLabels).not.toContain('Files')
    expect(allLabels).not.toContain('To-dos')
  })

  test('should auto-expand archived section after archiving', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Archive the workspace
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // The archived section should be visible and expanded (auto-expand)
    const archivedSection = page.locator('[data-testid="section-header-workspaces_archived"]')
    await expect(archivedSection).toBeVisible()

    // The workspace item should be visible inside the archived section without
    // manually expanding it — proving the section was auto-expanded
    await expect(workspaceItem).toBeVisible()
  })

  test('should keep tabs visible after archiving active workspace', async ({ page, authenticatedWorkspace }) => {
    // The fixture auto-creates a workspace with an agent tab.
    // Verify at least one agent tab is visible before archiving.
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    await expect(agentTab).toBeVisible()

    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Archive the workspace
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // Tabs should still be visible (read-only) after archiving
    await expect(agentTab).toBeVisible()

    // Close button should be hidden (readOnly mode)
    await expect(agentTab.locator('[data-testid="tab-close"]')).not.toBeVisible()

    // The add-tab buttons should be hidden (workspace is archived)
    await expect(page.locator('[data-testid="new-agent-button"]')).not.toBeVisible()

    // Editor panel should be hidden for archived workspaces
    await expect(page.locator('[data-testid="agent-editor-panel"]')).not.toBeVisible()
  })

  test('should allow closing file tabs in archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'File Tab Close', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree and open a file tab
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })
      await page.getByText('package.json').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible({ timeout: 10_000 })

      // Archive the workspace
      await openWorkspaceContextMenu(page, 'File Tab Close')
      await page.getByRole('menuitem', { name: 'Archive' }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section to appear
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Agent tab close button should be hidden (readOnly mode)
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      await expect(agentTab.locator('[data-testid="tab-close"]')).not.toBeVisible()

      // File tab should still be visible with a close button
      await expect(fileTab).toBeVisible()
      const closeButton = fileTab.locator('[data-testid="tab-close"]')
      await expect(closeButton).toBeVisible()

      // Click the close button to close the file tab
      await closeButton.click()

      // File tab should be gone
      await expect(fileTab).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should hide tree mention button in archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'No Mention', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree to load
      const packageJsonNode = page.getByText('package.json')
      await expect(packageJsonNode).toBeVisible({ timeout: 15_000 })

      // Verify mention button IS visible before archive
      await packageJsonNode.hover()
      const treeRow = packageJsonNode.locator('..')
      const mentionButton = treeRow.locator('[data-testid="tree-mention-button"]')
      await expect(mentionButton).toBeVisible()

      // Move mouse away to close the hover
      await page.mouse.move(0, 0)

      // Archive the workspace
      await openWorkspaceContextMenu(page, 'No Mention')
      await page.getByRole('menuitem', { name: 'Archive' }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Hover over tree node — mention button should NOT be visible
      await packageJsonNode.hover()
      await expect(mentionButton).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should hide file mention button in archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'No File Mention', adminOrgId, process.cwd())
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for the file tree and open a file tab
      await expect(page.getByText('package.json')).toBeVisible({ timeout: 15_000 })
      await page.getByText('package.json').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible({ timeout: 10_000 })

      // Verify file mention button IS visible before archive
      const fileMentionButton = page.locator('[data-testid="file-mention-button"]')
      await expect(fileMentionButton).toBeVisible({ timeout: 5_000 })

      // Archive the workspace
      await openWorkspaceContextMenu(page, 'No File Mention')
      await page.getByRole('menuitem', { name: 'Archive' }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Click the file tab to view it again (it may have switched to agent tab)
      await fileTab.click()

      // File mention button should NOT be visible
      await expect(fileMentionButton).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should delete workspace using ConfirmDialog instead of native confirm', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = page.locator(`[data-testid="workspace-item-${authenticatedWorkspace.workspaceId}"]`)
    const workspaceName = await workspaceItem.textContent()

    // Navigate away so we can see delete result
    await page.goto('/o/admin')
    await expect(page.getByText(workspaceName!.trim())).toBeVisible()

    // Open context menu and click Delete
    await openWorkspaceContextMenu(page, workspaceName!.trim())
    await page.getByRole('menuitem', { name: 'Delete' }).click()

    // ConfirmDialog should appear (not native dialog)
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Delete Workspace')).toBeVisible()

    // Confirm the delete (need to click twice due to ConfirmButton danger mode)
    await dialog.getByRole('button', { name: 'Delete' }).click() // arms
    await dialog.getByRole('button', { name: 'Confirm?' }).click() // confirms

    // Workspace should be gone
    await expect(page.getByText(workspaceName!.trim())).not.toBeVisible()
  })
})
