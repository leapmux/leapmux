import type { ServerInfo } from './fixtures'
import { focusActiveTerminal } from './helpers/terminal'
import { openTerminalViaUI, renameTabViaUI, reopenWorkspace, sidebarLeaves, waitForLayoutSave } from './helpers/ui'
import { listTerminalsViaAPI } from './helpers/worktree'
import { expect, restartHub, restartWorker, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

/** Wait for the Worker to store the title before its process stops. */
async function waitForSavedTerminalTitle(server: Pick<ServerInfo, 'hubUrl' | 'adminToken' | 'workerId'>, workspaceId: string, title: string): Promise<void> {
  await expect.poll(async () => {
    const terminals = await listTerminalsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
    return terminals.map(terminal => terminal.title)
  }, 'the renamed title must reach the Worker before the restart').toContain(title)
}

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve terminal tab title after full restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    // Listen for the layout save before the terminal opens.
    const saved = waitForLayoutSave(page)

    // Open a terminal through the tab bar.
    await openTerminalViaUI(page)

    // Wait for the terminal tab and xterm to appear.
    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for the layout save to store the tab.
    await saved

    // Rename the terminal to save its title.
    // Escape sequences supply live titles that the Worker does not store.
    // The SignalTitle handler in backend/internal/worker/service/terminal.go sends that value.
    await renameTabViaUI(page, terminalTab, 'My Custom Title')

    // The tab bar updates before the Worker stores the title.
    // Layout persistence carries no title, so it cannot establish this receipt.
    await waitForSavedTerminalTitle(separateHubWorker, authenticatedWorkspace.workspaceId, 'My Custom Title')

    // Stop the Worker, then stop the Hub.
    await stopWorker(separateHubWorker)
    await stopHub(separateHubWorker)

    // Start the Hub, then start the Worker.
    await restartHub(separateHubWorker)
    await restartWorker(separateHubWorker)

    // Reload the page. The app restores the workspace from browser storage.
    await reopenWorkspace(page, authenticatedWorkspace.workspaceId)

    // Require the restored terminal tab and its stored title.
    const restoredTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(restoredTab).toBeVisible()
    await expect(restoredTab).toContainText('My Custom Title')
  })

  test('should recover exited terminal title and screen after reloading before worker reconnects', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const saved = waitForLayoutSave(page)

    await openTerminalViaUI(page)

    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()
    await saved

    const terminalId = await terminalTab.getAttribute('data-tab-id')
    expect(terminalId).toBeTruthy()

    // Rename the tab to store its title.
    await renameTabViaUI(page, terminalTab, 'Recovered Title')

    // The tab bar updates before the Worker stores the title.
    // Layout persistence carries no title, so it cannot establish this receipt.
    await waitForSavedTerminalTitle(separateHubWorker, authenticatedWorkspace.workspaceId, 'Recovered Title')

    await focusActiveTerminal(page)
    await page.keyboard.type('echo EXITEDRESTORE\n', { delay: 30 })
    await page.waitForFunction(() => {
      const getText = Reflect.get(window, '__getActiveTerminalText')
      if (typeof getText !== 'function')
        return false
      const text: unknown = getText()
      return typeof text === 'string' && text.includes('EXITEDRESTORE')
    })

    await page.keyboard.press('Control+D')
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(separateHubWorker.hubUrl, separateHubWorker.adminToken, separateHubWorker.workerId, authenticatedWorkspace.workspaceId)
      return terminals.find(terminal => terminal.id === terminalId)?.exited
    }).toBe(true)

    await stopWorker(separateHubWorker)
    await stopHub(separateHubWorker)

    await restartHub(separateHubWorker)

    await reopenWorkspace(page, authenticatedWorkspace.workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    await restartWorker(separateHubWorker)

    const restoredTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(restoredTab).toContainText('Recovered Title')
    await page.waitForFunction(() => {
      const getText = Reflect.get(window, '__getActiveTerminalText')
      if (typeof getText !== 'function')
        return false
      const text: unknown = getText()
      return typeof text === 'string' && text.includes('EXITEDRESTORE')
    })

    const restoredLeaf = sidebarLeaves(page, authenticatedWorkspace.workspaceId)
      .and(page.locator(`[data-tab-id="${terminalId}"]:visible`))
      .first()
    await expect(restoredLeaf).toContainText('Recovered Title')
  })
})
