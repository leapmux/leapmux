import type { AgentServer } from './helpers/workspace'
import { typeInTerminal, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, renameTabViaUI, reopenWorkspace, sidebarLeaves, terminalTabs, waitForLayoutSave } from './helpers/ui'
import { listTerminalsViaAPI, waitForTerminalExitViaAPI, waitForWorkerTabTitle } from './helpers/workerTabs'
import { expect, restartHub, restartWorker, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

/** Wait for the Worker to store the title before its process stops. */
async function waitForSavedTerminalTitle(server: AgentServer, workspaceId: string, title: string): Promise<void> {
  await waitForWorkerTabTitle(
    () => listTerminalsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId),
    title,
    'the renamed title must reach the Worker before the restart',
  )
}

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve terminal tab title after full restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    // Listen for the layout save before the terminal opens.
    const saved = waitForLayoutSave(page)

    // Open a terminal through the tab bar. The helper waits for its tab and its xterm.
    await openTerminalViaUI(page)
    const terminalTab = terminalTabs(page)

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
    const restoredTab = terminalTabs(page)
    await expect(restoredTab).toBeVisible()
    await expect(restoredTab).toContainText('My Custom Title')
  })

  test('should recover exited terminal title and screen after reloading before worker reconnects', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const saved = waitForLayoutSave(page)

    const terminalId = await openTerminalViaUI(page)
    const terminalTab = terminalTabs(page)
    await saved

    // Rename the tab to store its title.
    await renameTabViaUI(page, terminalTab, 'Recovered Title')

    // The tab bar updates before the Worker stores the title.
    // Layout persistence carries no title, so it cannot establish this receipt.
    await waitForSavedTerminalTitle(separateHubWorker, authenticatedWorkspace.workspaceId, 'Recovered Title')

    await typeInTerminal(page, 'echo EXITEDRESTORE')
    await waitForTerminalText(page, 'EXITEDRESTORE')

    await page.keyboard.press('Control+D')
    await waitForTerminalExitViaAPI(separateHubWorker, authenticatedWorkspace.workspaceId, terminalId)

    await stopWorker(separateHubWorker)
    await stopHub(separateHubWorker)

    await restartHub(separateHubWorker)

    await reopenWorkspace(page, authenticatedWorkspace.workspaceId)
    await expect(terminalTabs(page)).toBeVisible()

    await restartWorker(separateHubWorker)

    await expect(terminalTabs(page)).toContainText('Recovered Title')
    await waitForTerminalText(page, 'EXITEDRESTORE')

    const restoredLeaf = sidebarLeaves(page, authenticatedWorkspace.workspaceId)
      .and(page.locator(`[data-tab-id="${terminalId}"]:visible`))
      .first()
    await expect(restoredLeaf).toContainText('Recovered Title')
  })
})
