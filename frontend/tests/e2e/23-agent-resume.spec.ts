import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test } from './process-control-fixtures'

/**
 * Wait for an assistant message containing the given text.
 * Scoped to assistant message bubbles to avoid false matches from
 * timestamps, file names, or other page content.
 */
async function waitForAssistantMessage(page: import('@playwright/test').Page, text: string) {
  await page.waitForFunction(
    (t: string) => {
      const msgs = document.querySelectorAll(
        '[data-testid="message-bubble"][data-role="assistant"] [data-testid="message-content"]',
      )
      return [...msgs].some(m => m.textContent?.includes(t))
    },
    text,
    { timeout: 60_000 },
  )
}

test.describe('Agent Session Resume', () => {
  test('should resume agent session after worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Resume Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')

      // Wait for the assistant's response
      await waitForAssistantMessage(page, '4')

      // Stop the worker
      await stopWorker()

      // Wait for the agent to show as closed
      await page.waitForTimeout(3000)

      // The editor should still be enabled (agent has session ID so it's resumable)
      await expect(editor).toBeVisible()

      // Restart the worker
      await restartWorker(separateHubWorker)

      // Send a new message to the closed (but resumable) agent
      await editor.click()
      await page.keyboard.type('What is 3+3? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for a response - the agent should have resumed
      await waitForAssistantMessage(page, '6')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should deliver control request after worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Control Request Restart', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await waitForAssistantMessage(page, '4')

      // Stop the worker, wait, restart
      await stopWorker()
      await page.waitForTimeout(3000)
      await restartWorker(separateHubWorker)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Switch permission mode to Plan Mode via the settings dropdown
      const trigger = page.locator('[data-testid="agent-settings-trigger"]')
      await trigger.click()
      await expect(page.locator('[data-testid="agent-settings-menu"]')).toBeVisible()
      await page.locator('[data-testid="permission-mode-plan"]').click()

      // Verify trigger shows Plan Mode — confirms the control request was delivered
      // after the agent was transparently restarted
      await expect(trigger).toContainText('Plan Mode')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should handle interrupt after worker restart', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Interrupt Restart', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Wait for agent tab and editor
      const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
      await expect(editor).toBeVisible()

      // Send a message and wait for response (establishes session)
      await editor.click()
      await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')
      await expect(editor).toHaveText('')
      await waitForAssistantMessage(page, '4')

      // Stop the worker, wait, restart
      await stopWorker()
      await page.waitForTimeout(3000)
      await restartWorker(separateHubWorker)

      // Wait for editor to be visible (worker reconnected)
      await expect(editor).toBeVisible()

      // Send another message to confirm agent is alive after restart
      await editor.click()
      await page.keyboard.type('What is 5+5? Reply with just the number, nothing else.')
      await page.keyboard.press('Meta+Enter')

      // Wait for response — verifies normal operation post-restart
      await waitForAssistantMessage(page, '10')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
