import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { focusActiveTerminal } from './helpers/terminal'
import { ARITHMETIC_PROMPT, assistantBubbles, expectAnyVisible, expectAssistantAnswer, expectUserMessage, loginViaToken, openTerminalViaUI, openWorkspace, renameTabViaUI, reopenWorkspace, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_PROMPT, waitForLayoutSave } from './helpers/ui'
import { listTerminalsViaAPI } from './helpers/worktree'
import { ensureWorkerOnline, expect, restartHub, restartWorker, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve chat history after hub and worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Full Restart Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response
      await editor.click()
      await page.keyboard.type(ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "6912"
      await expectAssistantAnswer(page)

      // Verify the user message is also visible
      await expectUserMessage(page, '1234 + 5678')

      // Step 2: Stop Worker first (so agent is terminated), then stop Hub
      await stopWorker()
      await stopHub()

      // Step 3: Start Hub and Worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload to establish fresh connections to the restarted Hub. The app
      // restores the workspace from localStorage — there is no URL to carry it.
      await reopenWorkspace(page, workspaceId)

      // Wait for the editor to be ready after page reload
      await expect(editor).toBeVisible()

      // Verify the first conversation is still visible after restart (loaded from DB)
      await expectUserMessage(page, '1234 + 5678')
      await expectAssistantAnswer(page)

      // Step 4: Send another message and wait for response. The second answer
      // ("3333") must not be a substring of the first ("6912"), otherwise this
      // wait would match the leftover first-turn bubble instead of the new one.
      await editor.click()
      await page.keyboard.type(SECOND_ARITHMETIC_PROMPT)
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response containing "3333"
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

      // Step 5: Verify both conversations are visible in chat history.
      await expectUserMessage(page, '1234 + 5678')
      await expectUserMessage(page, '1111 + 2222')

      // Verify both assistant responses are present. The two answers ("6912"
      // and "3333") are mutually non-substring, so each check matches only its
      // own turn.
      await expectAssistantAnswer(page)
      await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should preserve terminal tab title after full restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId } = separateHubWorker
    // Listen for layout save before opening terminal
    const saved = waitForLayoutSave(page)

    // Open a terminal via the tab bar
    await openTerminalViaUI(page)

    // Wait for the terminal tab to appear and xterm to render
    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for layout save so the tab is persisted
    await saved

    // Rename the tab. A rename is the only thing that writes a terminal's
    // PERSISTED title: a PTY-driven OSC title is broadcast as a live overlay
    // and deliberately never written to the DB (see the SignalTitle case in
    // the worker's terminal.go), so it could not survive a restart by design.
    await renameTabViaUI(page, terminalTab, 'My Custom Title')

    // Wait for the WORKER to hold the new title before tearing anything down.
    // `renameTabViaUI` only proves the TAB BAR updated, and that text comes
    // from the local metadata patch the rename handler applies before it fires
    // `UpdateTerminalTitle` without awaiting it. Stopping the worker inside
    // that window drops the durable write, and the restored-title assertion
    // below then fails as if persistence had regressed. `waitForLayoutSave`
    // does not cover this -- the CRDT layout carries no title.
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
      return terminals.map(t => t.title)
    }, 'the renamed title must reach the worker before the restart').toContain('My Custom Title')

    // Stop worker first, then hub
    await stopWorker()
    await stopHub()

    // Start hub and worker back up
    await restartHub(separateHubWorker)
    await restartWorker(separateHubWorker)

    // Reload; the app restores the workspace from localStorage.
    await reopenWorkspace(page, authenticatedWorkspace.workspaceId)

    // Verify the terminal tab is restored with the custom title
    const restoredTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(restoredTab).toBeVisible()
    await expect(restoredTab).toContainText('My Custom Title')
  })

  test('should recover exited terminal title and screen after reloading before worker reconnects', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const saved = waitForLayoutSave(page)

    await openTerminalViaUI(page)

    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()
    await saved

    const terminalId = await terminalTab.getAttribute('data-tab-id')
    expect(terminalId).toBeTruthy()

    // See the note in the previous test: only a rename persists.
    await renameTabViaUI(page, terminalTab, 'Recovered Title')

    // Wait for the WORKER to hold the new title before tearing anything down.
    // `renameTabViaUI` only proves the TAB BAR updated, and that text comes
    // from the local metadata patch the rename handler applies before it fires
    // `UpdateTerminalTitle` without awaiting it. Stopping the worker inside
    // that window drops the durable write, and the restored-title assertion
    // below then fails as if persistence had regressed. `waitForLayoutSave`
    // does not cover this -- the CRDT layout carries no title.
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
      return terminals.map(t => t.title)
    }, 'the renamed title must reach the worker before the restart').toContain('Recovered Title')

    await focusActiveTerminal(page)
    await page.keyboard.type('echo EXITEDRESTORE\n', { delay: 30 })
    await page.waitForFunction(() => {
      return typeof (window as any).__getActiveTerminalText === 'function'
        && ((window as any).__getActiveTerminalText() as string).includes('EXITEDRESTORE')
    })

    await page.keyboard.press('Control+D')
    await page.waitForTimeout(2000)

    await stopWorker()
    await stopHub()

    await restartHub(separateHubWorker)

    await reopenWorkspace(page, authenticatedWorkspace.workspaceId)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    await restartWorker(separateHubWorker)

    const restoredTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(restoredTab).toContainText('Recovered Title')
    await page.waitForFunction(() => {
      return typeof (window as any).__getActiveTerminalText === 'function'
        && ((window as any).__getActiveTerminalText() as string).includes('EXITEDRESTORE')
    })

    const restoredLeaf = page.locator(`[data-testid="tab-tree-leaf"][data-tab-id="${terminalId}"]`)
    await expect(restoredLeaf).toContainText('Recovered Title')
  })

  test('should preserve agent tab after clicking it post-restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Tab Click Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Verify the agent tab is visible
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toHaveCount(1)

      // Stop worker and hub
      await stopWorker()
      await stopHub()

      // Restart hub and worker
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload; the app restores the workspace from localStorage.
      await reopenWorkspace(page, workspaceId)

      // Agent tab should be visible after restore
      await expect(agentTab).toHaveCount(1)

      // Click the agent tab — it should remain visible (not disappear).
      // Before the fix, clicking an inactive agent with no messages would
      // remove it because the WatchEvents catch-up phase reported INACTIVE
      // status before message replay completed.
      await agentTab.click()
      await page.waitForTimeout(2000)
      await expect(agentTab).toHaveCount(1)

      // Also verify the tab tree leaf is present in the sidebar
      const treeLeaf = page.locator('[data-testid="tab-tree-leaf"]')
      await expect(treeLeaf).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should not show thinking indicator after full restart during active turn', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Thinking Test')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a long message to start an agent turn
      await editor.click()
      await page.keyboard.type('Write a very long essay about the history of computing. Make it extremely detailed.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the thinking indicator or streaming to appear (agent is processing)
      const thinkingIndicator = page.locator('[data-testid="thinking-indicator"]')
      const streamingText = assistantBubbles(page)
      await expectAnyVisible(thinkingIndicator, streamingText)

      // Stop worker first (so agent is terminated), then stop hub — while agent is mid-turn
      await stopWorker()
      await stopHub()

      // Start hub and worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload to establish fresh connections to the restarted hub. The app
      // restores the workspace from localStorage — there is no URL to carry it.
      await reopenWorkspace(page, workspaceId)
      await expect(editor).toBeVisible()

      // Thinking indicator should NOT be visible — stale ACTIVE agents
      // are closed on hub startup so the frontend sees INACTIVE status.
      await expect(thinkingIndicator).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
