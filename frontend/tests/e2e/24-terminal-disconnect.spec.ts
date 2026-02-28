import type { Page } from '@playwright/test'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, stopWorker, processTest as test } from './process-control-fixtures'

/** Read terminal text content from the active xterm's buffer. */
async function getTerminalText(page: Page): Promise<string> {
  return page.evaluate(() => {
    if (typeof (window as any).__getActiveTerminalText === 'function') {
      return (window as any).__getActiveTerminalText() as string
    }
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.style.display !== 'none') {
        const rows = container.querySelector('.xterm-rows')
        if (rows)
          return rows.textContent ?? ''
      }
    }
    return document.querySelector('.xterm-rows')?.textContent ?? ''
  })
}

test.describe('Terminal Disconnection', () => {
  test('should mark terminal as disconnected when worker stops', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId, adminOrgId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Terminal Disconnect Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open a terminal tab
      await page.locator('[data-testid="new-terminal-button"]').click()

      // Wait for the terminal tab to appear and be active
      const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
      await expect(terminalTab).toBeVisible()
      await terminalTab.click()

      // Wait for the terminal to be ready (xterm.js renders)
      await page.waitForTimeout(2000)

      // Stop the worker
      await stopWorker()

      // Verify the Hub reports the worker as offline via API
      const token = await page.evaluate(() => localStorage.getItem('leapmux_token'))
      await expect(async () => {
        const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
          method: 'POST',
          headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
          body: '{}',
        })
        const data = await res.json() as { workers: Array<{ online: boolean }> }
        expect(data.workers?.[0]?.online).toBeFalsy()
      }).toPass()

      // Wait for the disconnection message to appear in xterm buffer.
      await expect(async () => {
        const text = await getTerminalText(page)
        expect(text).toContain('Connection to the terminal was lost')
      }).toPass()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
