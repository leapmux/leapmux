import path from 'node:path'
import { AgentStatus } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { cleanupWorkspaceViaAPI, createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { sendActiveTerminalInput, typeInTerminal, waitForTerminalText } from './helpers/terminal'
import { ARITHMETIC_PROMPT, expectAssistantAnswer, loginViaToken, openTerminalViaUI, openTreeContextMenu, openWorkspace, treeRow, workspaceRow } from './helpers/ui'
import { listAgentsViaAPI, listTerminalsViaAPI } from './helpers/worktree'
import { ensureWorkerOnline, processTest, restartWorker, stopWorker, waitForWorkerOffline } from './process-control-fixtures'

const frontendDir = path.resolve(import.meta.dirname, '../..')

/**
 * The workspace row menu carries an INFO BLOCK and, once the workspace spans
 * more than one repository, a row named after each repository. Playwright
 * matches an accessible name by SUBSTRING unless told otherwise -- so
 * `{ name: 'Delete' }` also matched the info block, whose name is every row of
 * it joined, and a repository named after a branch matched the branch items.
 * Every lookup below is `exact`.
 */

/** Open the context menu for a workspace item that's already located by testid. */
async function openContextMenu(item: ReturnType<import('@playwright/test').Page['locator']>) {
  await item.hover()
  await item.locator('button').first().click()
}

test.describe('workspace archive', () => {
  test('should archive workspace via context menu with confirmation dialog', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Open context menu and click Archive (top-level menu item)
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()

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
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Open context menu and click Archive (top-level menu item)
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()

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
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Archive the workspace first: open context menu using the workspace item directly
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // Wait for the archived section to appear (auto-expanded)
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()
    await expect(workspaceItem).toBeVisible()

    // Now unarchive it: open context menu using the workspace item directly
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Unarchive', exact: true }).click()

    // Workspace is active again — add-tab buttons should be visible
    await expect(page.locator('[data-testid^="new-agent-button"]').first()).toBeVisible()
  })

  test('should not show Move-to when workspace is in the only target section', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Open context menu — with only one workspace section (In Progress),
    // "Move to" should not appear since there are no other target sections
    await openContextMenu(workspaceItem)

    // "Move to" should not be visible (no other non-archived, non-shared sections to move to)
    await expect(page.getByRole('menuitem', { name: 'Move to', exact: true })).not.toBeVisible()

    // Other menu items should be present
    await expect(page.getByRole('menuitem', { name: 'Rename', exact: true })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Archive', exact: true })).toBeVisible()
  })

  test('leaks no non-workspace section into the row menu', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    await openContextMenu(workspaceItem)

    // Files and To-dos are sections, but not ones a workspace can live in, so
    // no item of this menu may name them.
    //
    // This does NOT exercise the Move-to submenu, and its old name claimed it
    // did. The submenu mounts its items only while it is open (see `SubMenu`),
    // and this fixture has one workspace section, so Move-to is not offered at
    // all. `isMoveTargetSection`'s filter is covered where it can actually be
    // driven: `WorkspaceContextMenu.test.tsx` ("lists every other workspace
    // section, and no other kind"), and in a real browser by 195's
    // "a custom section can be created, renamed and deleted from the menu",
    // which creates the second section the submenu needs and then opens it.
    const allLabels = await page.getByRole('menuitem').allTextContents()
    expect(allLabels).not.toContain('Files')
    expect(allLabels).not.toContain('To-dos')
  })

  test('should auto-expand archived section after archiving', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Archive the workspace
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
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

    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Archive the workspace
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

    // Tabs should still be visible (read-only) after archiving
    await expect(agentTab).toBeVisible()

    // Close button should be hidden (readOnly mode)
    await expect(agentTab.locator('[data-testid="tab-close"]')).not.toBeVisible()

    // The add-tab buttons should be hidden (workspace is archived)
    await expect(page.locator('[data-testid^="new-agent-button"]')).not.toBeVisible()

    // Editor panel should be hidden for archived workspaces
    await expect(page.locator('[data-testid="agent-editor-panel"]')).not.toBeVisible()
  })

  test('stops processes, preserves content, and resumes only the agent', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = authenticatedWorkspace.workspaceId
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    const agentId = await agentTab.getAttribute('data-tab-id')
    expect(agentId).toBeTruthy()
    await expect.poll(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      return agents.find(agent => agent.id === agentId)?.status
    }).toBe(AgentStatus.ACTIVE)
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type(ARITHMETIC_PROMPT)
    await page.keyboard.press('Meta+Enter')
    await expectAssistantAnswer(page)

    await openTerminalViaUI(page)
    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]').first()
    await expect(terminalTab).toBeVisible()
    const terminalId = await terminalTab.getAttribute('data-tab-id')
    expect(terminalId).toBeTruthy()
    await typeInTerminal(page, 'echo ARCHIVE_SCREEN_PRESERVED')
    await waitForTerminalText(page, 'ARCHIVE_SCREEN_PRESERVED')

    const workspaceItem = workspaceRow(page, workspaceId)
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
    await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()
    await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

    await expect.poll(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      return agents.find(agent => agent.id === agentId)?.status
    }).toBe(AgentStatus.INACTIVE)
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      return terminals.find(terminal => terminal.id === terminalId)?.exited
    }).toBe(true)
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

    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Unarchive', exact: true }).click()
    await expect.poll(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      return agents.find(agent => agent.id === agentId)?.status
    }).toBe(AgentStatus.ACTIVE)
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, workspaceId)
      return terminals.find(terminal => terminal.id === terminalId)?.exited
    }).toBe(true)
  })

  test('should keep file tabs uncloseable in an archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Tab Close')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for the file tree and open a file tab
      await expect(treeRow(page, 'package.json')).toBeVisible()
      await treeRow(page, 'package.json').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toBeVisible()

      // Archive the workspace
      const wsItem = workspaceRow(page, workspaceId)
      await openContextMenu(wsItem)
      await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section to appear
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Agent tab close button should be hidden (readOnly mode)
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      await expect(agentTab.locator('[data-testid="tab-close"]')).not.toBeVisible()

      // The file tab stays visible, but archival blocks every tab mutation.
      await expect(fileTab).toBeVisible()
      const closeButton = fileTab.locator('[data-testid="tab-close"]')
      await expect(closeButton).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should hide tree mention button in archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'No Mention')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for the file tree to load
      const row = treeRow(page, 'package.json')
      await expect(row).toBeVisible()

      // Verify mention button IS visible before archive (via context menu)
      const mentionButton = page.locator('[data-testid="tree-mention-button"]:visible')
      await openTreeContextMenu(page, row, 'tree-mention-button')
      // Close menu by pressing Escape
      await page.keyboard.press('Escape')
      await expect(mentionButton).toHaveCount(0)

      // Move mouse away
      await page.mouse.move(0, 0)

      // Archive the workspace
      const wsItem = workspaceRow(page, workspaceId)
      await openContextMenu(wsItem)
      await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Open the context menu again — the mention entry must be gone, but the
      // menu itself must still open, so assert on an item that SURVIVES
      // archiving. Without that anchor a menu that failed to open at all would
      // satisfy "mention button not visible" for the wrong reason.
      await openTreeContextMenu(page, row)
      await expect(mentionButton).toHaveCount(0)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should hide file mention button in archived workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'No File Mention')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

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

      // Archive the workspace
      const wsItem = workspaceRow(page, workspaceId)
      await openContextMenu(wsItem)
      await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()

      // Wait for archived section
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      // Click the file tab to view it again (it may have switched to agent tab)
      await fileTab.click()

      // The actions menu still exists (save/copy items), but the mention
      // item must be gone in an archived workspace.
      await fileActionsTrigger.click()
      await expect(fileMentionButton).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should delete workspace using ConfirmDialog instead of native confirm', async ({ page, authenticatedWorkspace }) => {
    const workspaceItem = workspaceRow(page, authenticatedWorkspace.workspaceId)

    // Navigate away so we can see delete result
    await page.goto('/')
    await expect(workspaceItem).toBeVisible()

    // Open context menu and click Delete
    await openContextMenu(workspaceItem)
    await page.getByRole('menuitem', { name: 'Delete', exact: true }).click()

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
  processTest('prevents agent resume when archival happens while the Worker is offline', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Offline Archive')
    const agentId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)
      await expect.poll(async () => {
        const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
        return agents.find(agent => agent.id === agentId)?.status
      }).toBe(AgentStatus.ACTIVE)
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await editor.click()
      await page.keyboard.type(ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')
      await expectAssistantAnswer(page)

      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)
      const workspaceItem = workspaceRow(page, workspaceId)
      await openContextMenu(workspaceItem)
      await page.getByRole('menuitem', { name: 'Archive', exact: true }).click()
      await page.locator('dialog').getByRole('button', { name: 'Archive' }).click()
      await expect(page.locator('[data-testid="section-header-workspaces_archived"]')).toBeVisible()

      await restartWorker(separateHubWorker)
      await expect.poll(async () => {
        const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId)
        return agents.find(agent => agent.id === agentId)?.status
      }).toBe(AgentStatus.INACTIVE)
    }
    finally {
      await restartWorker(separateHubWorker).catch(() => {})
      await cleanupWorkspaceViaAPI(hubUrl, adminToken, workerId, workspaceId).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
