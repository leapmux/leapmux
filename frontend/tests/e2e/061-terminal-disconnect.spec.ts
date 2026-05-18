import { authedHeaders, createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { getTerminalText, sendActiveTerminalInput } from './helpers/terminal'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, stopWorker, processTest as test } from './process-control-fixtures'

test.describe('Terminal Disconnection', () => {
  test('should mark terminal as disconnected when worker stops', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Terminal Disconnect Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
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
      await expect(async () => {
        const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
          method: 'POST',
          headers: authedHeaders(adminToken),
          body: '{}',
        })
        const data = await res.json() as { workers: Array<{ online: boolean }> }
        expect(data.workers?.[0]?.online).toBeFalsy()
      }).toPass()

      // Wait for the exit notice to appear in xterm buffer. Worker
      // shutdown forcibly tears down children before reaping their exit
      // codes, so the notice carries the "Worker disconnected" wording
      // (no exit code) rather than a literal "?" — the worker knows it
      // killed the child, so the cause isn't actually unknown.
      await expect(async () => {
        const text = await getTerminalText(page)
        expect(text).toContain('Worker disconnected - Press Enter to restart')
      }).toPass()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should restart the shell when the user presses Enter on an exited terminal', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, adminOrgId, workerId } = separateHubWorker
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Terminal Restart Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      await page.locator('[data-testid="new-terminal-button"]').click()
      const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
      await expect(terminalTab).toBeVisible()
      await terminalTab.click()

      // Wait until xterm has rendered the first prompt before sending
      // input, otherwise the keystrokes can race the PTY's first
      // SIGWINCH and the shell never sees the bytes.
      await expect(async () => {
        const text = await getTerminalText(page)
        expect(text.trim().length).toBeGreaterThan(0)
      }).toPass()

      // Print a marker that identifies the *first* shell session, then
      // exit it. The marker survives in the xterm buffer across the
      // restart so we can confirm the buffer wasn't cleared.
      expect(await sendActiveTerminalInput(page, 'echo first_session_marker\r')).toBe(true)
      await expect(async () => {
        const text = await getTerminalText(page)
        // Wait for the first command to be echoed back AND its output to
        // appear ("first_session_marker" doubled — once for the echo of
        // the typed command, once for echo's stdout).
        expect((text.match(/first_session_marker/g) ?? []).length).toBeGreaterThanOrEqual(2)
      }).toPass()

      expect(await sendActiveTerminalInput(page, 'exit\r')).toBe(true)

      // Notice should appear with exit code 0 (clean `exit`).
      await expect(async () => {
        const text = await getTerminalText(page)
        expect(text).toContain('Terminal process exited (0) - Press Enter to restart')
      }).toPass()

      // Press Enter via the same callback the keyboard would fire —
      // handleTerminalInput sees the CR on EXITED and calls
      // restartTerminal.
      expect(await sendActiveTerminalInput(page, '\r')).toBe(true)

      // Wait for the new shell's prompt to render past the notice.
      await expect(async () => {
        const text = await getTerminalText(page)
        const exitIdx = text.indexOf('Terminal process exited (0) - Press Enter to restart')
        expect(exitIdx).toBeGreaterThanOrEqual(0)
        const afterNotice = text.slice(exitIdx + 'Terminal process exited (0) - Press Enter to restart]'.length)
        expect(afterNotice.trim().length).toBeGreaterThan(0)
      }).toPass()

      // Send the second marker into the restarted shell.
      expect(await sendActiveTerminalInput(page, 'echo second_session_marker\r')).toBe(true)

      await expect(async () => {
        const text = await getTerminalText(page)
        expect(text).toContain('first_session_marker')
        expect(text).toContain('Terminal process exited (0) - Press Enter to restart')
        expect((text.match(/second_session_marker/g) ?? []).length).toBeGreaterThanOrEqual(2)
      }).toPass()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
