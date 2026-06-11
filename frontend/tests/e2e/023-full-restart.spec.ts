import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { ASSISTANT_BUBBLE_SELECTOR, assistantBubbles, expectAnyVisible, loginViaToken, waitForLayoutSave, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, restartHub, restartWorker, stopHub, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Full Hub+Worker Restart', () => {
  test('should preserve chat history after hub and worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Full Restart Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Step 1: Send a message and wait for a response
      await editor.click()
      await page.keyboard.type('What is 1234 + 5678? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response containing "6912"
      await page.waitForFunction((sel: string) => {
        const bubbles = document.querySelectorAll(sel)
        for (const b of bubbles) {
          if (/6,?912/.test(b.textContent ?? ''))
            return true
        }
        return false
      }, ASSISTANT_BUBBLE_SELECTOR)

      // Verify the user message is also visible
      const userBubbles = page.locator('[data-testid="message-bubble"][data-role="user"]')
      await expect(userBubbles.first()).toContainText('1234 + 5678')

      // Remember the workspace URL so we can navigate back after restart
      const workspaceUrl = page.url()

      // Step 2: Stop Worker first (so agent is terminated), then stop Hub
      await stopWorker()
      await stopHub()

      // Step 3: Start Hub and Worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload the page to establish fresh connections to the restarted Hub.
      await page.goto(workspaceUrl)

      // Wait for the editor to be ready after page reload
      await expect(editor).toBeVisible()

      // Verify the first conversation is still visible after restart (loaded from DB)
      await page.waitForFunction((sel: string) => {
        const userBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="user"]')
        const asstBubbles = document.querySelectorAll(sel)
        let hasUserMsg = false
        let hasAssistantResp = false
        for (const b of userBubbles) {
          if (b.textContent?.includes('1234 + 5678'))
            hasUserMsg = true
        }
        for (const b of asstBubbles) {
          if (/6,?912/.test(b.textContent ?? ''))
            hasAssistantResp = true
        }
        return hasUserMsg && hasAssistantResp
      }, ASSISTANT_BUBBLE_SELECTOR)

      // Step 4: Send another message and wait for response. The second answer
      // ("3333") must not be a substring of the first ("6912"), otherwise this
      // wait would match the leftover first-turn bubble instead of the new one.
      await editor.click()
      await page.keyboard.type('What is 1111 + 2222? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for the assistant's response containing "3333"
      await page.waitForFunction((sel: string) => {
        const bubbles = document.querySelectorAll(sel)
        for (const b of bubbles) {
          if (/3,?333/.test(b.textContent ?? ''))
            return true
        }
        return false
      }, ASSISTANT_BUBBLE_SELECTOR)

      // Step 5: Verify both conversations are visible in chat history.
      await page.waitForFunction(() => {
        const userBubbles = document.querySelectorAll('[data-testid="message-bubble"][data-role="user"]')
        let hasFirstQuestion = false
        let hasSecondQuestion = false
        for (const b of userBubbles) {
          const text = b.textContent || ''
          if (text.includes('1234 + 5678'))
            hasFirstQuestion = true
          if (text.includes('1111 + 2222'))
            hasSecondQuestion = true
        }
        return hasFirstQuestion && hasSecondQuestion
      })

      // Verify both assistant responses are present. The two answers ("6912"
      // and "3333") are mutually non-substring, so each check matches only its
      // own turn.
      await page.waitForFunction((sel: string) => {
        const asstBubbles = document.querySelectorAll(sel)
        let hasFirstAnswer = false
        let hasSecondAnswer = false
        for (const b of asstBubbles) {
          const text = b.textContent || ''
          if (/6,?912/.test(text))
            hasFirstAnswer = true
          if (/3,?333/.test(text))
            hasSecondAnswer = true
        }
        return hasFirstAnswer && hasSecondAnswer
      }, ASSISTANT_BUBBLE_SELECTOR)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should preserve terminal tab title after full restart', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    // Listen for layout save before opening terminal
    const saved = waitForLayoutSave(page)

    // Open a terminal via the tab bar
    await page.locator('[data-testid="new-terminal-button"]').click()

    // Wait for the terminal tab to appear and xterm to render
    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for layout save so the tab is persisted
    await saved

    // Set the terminal title explicitly via an escape sequence.
    // This simulates what shells do automatically with precmd hooks.
    // Focus the terminal textarea and type the escape sequence.
    await page.evaluate(() => {
      const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
      for (const container of containers) {
        if (container.dataset.active === 'true') {
          const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
          if (textarea) {
            textarea.focus()
            return
          }
        }
      }
    })
    await page.keyboard.type('printf "\\e]0;My Custom Title\\a"\n', { delay: 30 })

    // Wait for the title to update in the tab
    await expect(terminalTab).toContainText('My Custom Title')

    // Wait for the UpdateTerminalTitle RPC to reach the backend
    await page.waitForTimeout(2000)

    const workspaceUrl = page.url()

    // Stop worker first, then hub
    await stopWorker()
    await stopHub()

    // Start hub and worker back up
    await restartHub(separateHubWorker)
    await restartWorker(separateHubWorker)

    // Reload the page
    await page.goto(workspaceUrl)

    // Verify the terminal tab is restored with the custom title
    const restoredTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(restoredTab).toBeVisible()
    await expect(restoredTab).toContainText('My Custom Title')
  })

  test('should recover exited terminal title and screen after reloading before worker reconnects', async ({ authenticatedWorkspace, separateHubWorker, page }) => {
    const saved = waitForLayoutSave(page)

    await page.locator('[data-testid="new-terminal-button"]').click()

    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()
    await saved

    const terminalId = await terminalTab.getAttribute('data-tab-id')
    expect(terminalId).toBeTruthy()

    await page.evaluate(() => {
      const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
      for (const container of containers) {
        if (container.dataset.active === 'true') {
          const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
          if (textarea) {
            textarea.focus()
            return
          }
        }
      }
    })
    await page.keyboard.type('printf "\\e]0;Recovered Title\\a"\n', { delay: 30 })
    await expect(terminalTab).toContainText('Recovered Title')

    await page.evaluate(() => {
      const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
      for (const container of containers) {
        if (container.dataset.active === 'true') {
          const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
          if (textarea) {
            textarea.focus()
            return
          }
        }
      }
    })
    await page.keyboard.type('echo EXITEDRESTORE\n', { delay: 30 })
    await page.waitForFunction(() => {
      return typeof (window as any).__getActiveTerminalText === 'function'
        && ((window as any).__getActiveTerminalText() as string).includes('EXITEDRESTORE')
    })

    await page.keyboard.press('Control+D')
    await page.waitForTimeout(2000)

    const workspaceUrl = page.url()

    await stopWorker()
    await stopHub()

    await restartHub(separateHubWorker)

    await page.goto(workspaceUrl)
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
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Tab Click Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Verify the agent tab is visible
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(agentTab).toHaveCount(1)

      const workspaceUrl = page.url()

      // Stop worker and hub
      await stopWorker()
      await stopHub()

      // Restart hub and worker
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload the page
      await page.goto(workspaceUrl)
      await waitForWorkspaceReady(page)

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
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Restart Thinking Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

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

      // Remember the workspace URL so we can navigate back after restart
      const workspaceUrl = page.url()

      // Stop worker first (so agent is terminated), then stop hub — while agent is mid-turn
      await stopWorker()
      await stopHub()

      // Start hub and worker back up
      await restartHub(separateHubWorker)
      await restartWorker(separateHubWorker)

      // Reload the page to establish fresh connections to the restarted hub
      await page.goto(workspaceUrl)
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
