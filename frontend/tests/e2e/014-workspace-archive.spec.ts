import { frontendRoot } from '~/test-support/sourceTree'
import { AgentStatus } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { withCleanup } from './helpers/cleanup'
import { sendScriptedTurn } from './helpers/scriptedTurn'
import { sendActiveTerminalInput, typeInTerminal, waitForTerminalText } from './helpers/terminal'
import {
  agentTabs,
  archiveWorkspaceViaUI,
  clickWorkspaceMenuItem,
  expectAssistantAnswer,
  openTerminalViaUI,
  openTreeContextMenu,
  openWorkspaceRowMenu,
  sidebarSectionHeader,
  terminalTabs,
  treeRow,
  workspaceMenuItem,
  workspaceRow,
} from './helpers/ui'
import { waitForAgentStatusViaAPI, waitForTerminalExitViaAPI } from './helpers/worktree'
import { ensureWorkerOnline, processTest, restartWorker, stopWorker, waitForWorkerOffline } from './process-control-fixtures'

/**
 * The workspace row menu carries an INFO BLOCK and, once the workspace spans
 * more than one repository, a row named after each repository. Playwright
 * matches an accessible name by SUBSTRING unless told otherwise -- so
 * `{ name: 'Delete' }` also matched the info block, whose name is every row of
 * it joined, and a repository named after a branch matched the branch items.
 * Every lookup below is `exact`, through `workspaceMenuItem`.
 */

test.describe('workspace archive', () => {
  // The file tree and the file tabs below read the frontend directory.
  test.use({ agentWorkingDir: frontendRoot })

  test('should archive workspace via context menu with confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace
    const workspaceItem = workspaceRow(page, workspaceId)

    // Open context menu and click Archive (top-level menu item)
    await clickWorkspaceMenuItem(page, workspaceId, 'Archive')

    // Confirmation dialog should appear
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Archive Workspace')).toBeVisible()
    await expect(dialog.getByText('All active agents and terminals will be stopped')).toBeVisible()

    // Confirm the archive
    await dialog.getByRole('button', { name: 'Archive' }).click()

    // Workspace should now be in the archived section (auto-expanded)
    const archivedSection = sidebarSectionHeader(page, 'workspaces_archived')
    await expect(archivedSection).toBeVisible()

    // Workspace item should be visible inside the archived section (auto-expanded)
    await expect(workspaceItem).toBeVisible()
  })

  test('should cancel archive via confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace
    const workspaceItem = workspaceRow(page, workspaceId)

    // Open context menu and click Archive (top-level menu item)
    await clickWorkspaceMenuItem(page, workspaceId, 'Archive')

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
    const { workspaceId } = authenticatedWorkspace

    // Archive the workspace first. The helper waits for the archived section.
    await archiveWorkspaceViaUI(page, workspaceId)
    await expect(workspaceRow(page, workspaceId)).toBeVisible()

    // Now unarchive it through the same row menu
    await clickWorkspaceMenuItem(page, workspaceId, 'Unarchive')

    // Workspace is active again — add-tab buttons should be visible
    await expect(page.locator('[data-testid^="new-agent-button"]').first()).toBeVisible()
  })

  test('should not show Move-to when workspace is in the only target section', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace

    // Open context menu — with only one workspace section (In Progress),
    // "Move to" should not appear since there are no other target sections
    await openWorkspaceRowMenu(page, workspaceId)

    // "Move to" should not be visible (no other non-archived, non-shared sections to move to)
    await expect(workspaceMenuItem(page, workspaceId, 'Move to')).not.toBeVisible()

    // Other menu items should be present
    await expect(workspaceMenuItem(page, workspaceId, 'Rename')).toBeVisible()
    await expect(workspaceMenuItem(page, workspaceId, 'Archive')).toBeVisible()
  })

  test('leaks no non-workspace section into the row menu', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace

    await openWorkspaceRowMenu(page, workspaceId)

    // Files and Goals & To-dos cannot hold workspaces and must not appear as move destinations.
    // This case has only one workspace section, so it does not open a Move-to submenu.
    // WorkspaceContextMenu.test.tsx verifies isMoveTargetSection with multiple sections.
    // Test 195 also creates another section and opens that submenu in the browser.
    // The items are read from this row's open menu: a role query skips the items of a closed menu.
    const allLabels = await workspaceRow(page, workspaceId).getByRole('menuitem').allTextContents()
    expect(allLabels).not.toContain('Files')
    expect(allLabels).not.toContain('Goals & To-dos')
  })

  test('should auto-expand archived section after archiving', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace

    // Archive the workspace. The helper requires the archived section to be visible.
    await archiveWorkspaceViaUI(page, workspaceId)

    // The workspace item should be visible inside the archived section without
    // manually expanding it — proving the section was auto-expanded
    await expect(workspaceRow(page, workspaceId)).toBeVisible()
  })

  test('should keep tabs visible after archiving active workspace', async ({ page, authenticatedWorkspace }) => {
    // The fixture auto-creates a workspace with an agent tab.
    // Verify at least one agent tab is visible before archiving.
    const agentTab = agentTabs(page).first()
    await expect(agentTab).toBeVisible()

    // Archive the workspace
    await archiveWorkspaceViaUI(page, authenticatedWorkspace.workspaceId)

    // Tabs should still be visible (read-only) after archiving
    await expect(agentTab).toBeVisible()

    // Close button should be hidden (readOnly mode)
    await expect(agentTab.locator('[data-testid="tab-close"]')).not.toBeVisible()

    // The add-tab buttons should be hidden (workspace is archived)
    await expect(page.locator('[data-testid^="new-agent-button"]')).not.toBeVisible()

    // Editor panel should be hidden for archived workspaces
    await expect(page.locator('[data-testid="agent-editor-panel"]')).not.toBeVisible()
  })

  test('stops processes, preserves content, and resumes only the agent', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    const { workspaceId, agentId } = authenticatedWorkspace
    const agentTab = agentTabs(page).first()
    await expect(agentTab).toHaveAttribute('data-tab-id', agentId)
    await waitForAgentStatusViaAPI(leapmuxServer, workspaceId, agentId, AgentStatus.ACTIVE)
    // The answer has to SURVIVE the archive and the resume below, so it is a
    // scripted turn: the transcript is the subject, and a live model would make
    // its content the variable this test cannot control.
    await sendScriptedTurn(page, modelScript)

    const terminalId = await openTerminalViaUI(page)
    const terminalTab = terminalTabs(page).first()
    await typeInTerminal(page, 'echo ARCHIVE_SCREEN_PRESERVED')
    await waitForTerminalText(page, 'ARCHIVE_SCREEN_PRESERVED')

    await archiveWorkspaceViaUI(page, workspaceId)

    await waitForAgentStatusViaAPI(leapmuxServer, workspaceId, agentId, AgentStatus.INACTIVE)
    await waitForTerminalExitViaAPI(leapmuxServer, workspaceId, terminalId)
    await expect(agentTab).toBeVisible()
    await expect(terminalTab).toBeVisible()
    await agentTab.click()
    await expectAssistantAnswer(page)
    await terminalTab.click()
    await waitForTerminalText(page, 'ARCHIVE_SCREEN_PRESERVED')

    await page.evaluate(() => {
      ;(window as unknown as { __archiveRestartCalls?: number }).__archiveRestartCalls = 0
      window.addEventListener('leapmux:rpc-send', ((event: CustomEvent<{ method?: string }>) => {
        if (event.detail?.method === 'RestartTerminal') {
          const state = window as unknown as { __archiveRestartCalls?: number }
          state.__archiveRestartCalls = (state.__archiveRestartCalls ?? 0) + 1
        }
      }) as EventListener)
    })
    expect(await sendActiveTerminalInput(page, '\r')).toBe(true)
    expect(await page.evaluate(() => (window as unknown as { __archiveRestartCalls?: number }).__archiveRestartCalls)).toBe(0)

    await clickWorkspaceMenuItem(page, workspaceId, 'Unarchive')
    await waitForAgentStatusViaAPI(leapmuxServer, workspaceId, agentId, AgentStatus.ACTIVE)
    await waitForTerminalExitViaAPI(leapmuxServer, workspaceId, terminalId)
  })

  test('should keep file tabs uncloseable in an archived workspace', async ({ page, authenticatedWorkspace }) => {
    // Wait for the file tree and open a file tab
    await expect(treeRow(page, 'package.json')).toBeVisible()
    await treeRow(page, 'package.json').click()
    const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
    await expect(fileTab).toBeVisible()

    // Archive the workspace. The helper waits for the archived section.
    await archiveWorkspaceViaUI(page, authenticatedWorkspace.workspaceId)

    // Agent tab close button should be hidden (readOnly mode)
    await expect(agentTabs(page).first().locator('[data-testid="tab-close"]')).not.toBeVisible()

    // The file tab stays visible, but archival blocks every tab mutation.
    await expect(fileTab).toBeVisible()
    const closeButton = fileTab.locator('[data-testid="tab-close"]')
    await expect(closeButton).not.toBeVisible()
  })

  test('should hide tree mention button in archived workspace', async ({ page, authenticatedWorkspace }) => {
    // Wait for the file tree to load
    const row = treeRow(page, 'package.json')
    await expect(row).toBeVisible()

    // Verify mention button IS visible before archive (via context menu)
    const mentionButton = page.locator('[data-testid="tree-mention-button"]:visible')
    await openTreeContextMenu(row, 'tree-mention-button')
    // Close menu by pressing Escape
    await page.keyboard.press('Escape')
    await expect(mentionButton).toHaveCount(0)

    // Move mouse away
    await page.mouse.move(0, 0)

    // Archive the workspace. The helper waits for the archived section.
    await archiveWorkspaceViaUI(page, authenticatedWorkspace.workspaceId)

    // Open the context menu again — the mention entry must be gone, but the
    // menu itself must still open, so assert on an item that SURVIVES
    // archiving. Without that anchor a menu that failed to open at all would
    // satisfy "mention button not visible" for the wrong reason.
    await openTreeContextMenu(row)
    await expect(mentionButton).toHaveCount(0)
  })

  test('should hide file mention button in archived workspace', async ({ page, authenticatedWorkspace }) => {
    // Wait for the file tree and open a file tab
    await expect(treeRow(page, 'package.json')).toBeVisible()
    await treeRow(page, 'package.json').click()
    const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
    await expect(fileTab).toBeVisible()

    // Verify the mention action IS available before archive. It lives in
    // the file viewer's actions dropdown, so open that first.
    const fileActionsTrigger = page.locator('[data-testid="file-actions-trigger"]')
    const fileMentionButton = page.locator('[data-testid="file-actions-mention-button"]')
    await fileActionsTrigger.click()
    await expect(fileMentionButton).toBeVisible()
    // Close the menu before interacting with the sidebar.
    await page.keyboard.press('Escape')

    // Archive the workspace. The helper waits for the archived section.
    await archiveWorkspaceViaUI(page, authenticatedWorkspace.workspaceId)

    // Click the file tab to view it again (it may have switched to agent tab)
    await fileTab.click()

    // The actions menu still exists (save/copy items), but the mention
    // item must be gone in an archived workspace.
    await fileActionsTrigger.click()
    await expect(fileMentionButton).not.toBeVisible()
  })

  test('should delete workspace using ConfirmDialog instead of native confirm', async ({ page, authenticatedWorkspace }) => {
    const { workspaceId } = authenticatedWorkspace
    const workspaceItem = workspaceRow(page, workspaceId)

    // Navigate away so we can see delete result
    await page.goto('/')
    await expect(workspaceItem).toBeVisible()

    // Open context menu and click Delete
    await clickWorkspaceMenuItem(page, workspaceId, 'Delete')

    // ConfirmDialog should appear (not native dialog)
    const dialog = page.locator('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText('Delete Workspace')).toBeVisible()

    // Confirm the delete (need to click twice due to ConfirmButton danger mode)
    await dialog.getByRole('button', { name: 'Delete' }).click() // arms
    await dialog.getByRole('button', { name: 'Confirm?' }).click() // confirms

    // Workspace should be gone
    await expect(workspaceItem).not.toBeVisible()
  })
})

processTest.describe('workspace archive reconciliation', () => {
  processTest.use({ agentWorkingDir: frontendRoot })

  processTest('prevents agent resume when archival happens while the Worker is offline', async ({ separateHubWorker, page, authenticatedWorkspace, modelScript }) => {
    const { workspaceId, agentId } = authenticatedWorkspace
    // This test stops the worker-scoped Worker. The cleanup brings it back after
    // a failure, so a later test of this Playwright worker does not fail for it.
    await withCleanup(async () => {
      await waitForAgentStatusViaAPI(separateHubWorker, workspaceId, agentId, AgentStatus.ACTIVE)
      await sendScriptedTurn(page, modelScript)

      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      await archiveWorkspaceViaUI(page, workspaceId)

      await restartWorker(separateHubWorker)
      await waitForAgentStatusViaAPI(separateHubWorker, workspaceId, agentId, AgentStatus.INACTIVE)
    }, () => ensureWorkerOnline(separateHubWorker))
  })
})
