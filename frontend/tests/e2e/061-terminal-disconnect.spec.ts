import { expect } from '@playwright/test'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TERMINAL_DISCONNECTED } from '~/stores/tab.types'
import { listOnlineWorkerIDsViaAPI } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { getTerminalRows, getTerminalText, sendActiveTerminalInput, waitForTerminalReady, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, terminalTabs } from './helpers/ui'
import { ensureWorkerOnline, restartHub, restartWorker, stopHub, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Terminal Disconnection', () => {
  test('restores terminal output and one disconnect notice after a Worker restart', async ({ separateHubWorker, page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await withCleanup(async () => {
      await openTerminalViaUI(page)

      await terminalTabs(page).click()
      await waitForTerminalReady(page)
      const marker = 'SHUTDOWN_RESTORE_MARKER'
      expect(await sendActiveTerminalInput(page, `echo ${marker.slice(0, 9)}""${marker.slice(9)}\r`)).toBe(true)
      await waitForTerminalText(page, marker)

      await stopWorker(separateHubWorker)
      await waitForWorkerOffline(separateHubWorker)
      await waitForTerminalText(page, 'Worker disconnected - Press Enter to restart')
      await restartWorker(separateHubWorker)
      await page.reload()
      await expect(terminalTabs(page)).toBeVisible()
      await terminalTabs(page).click()
      await expect(terminalTabs(page)).toHaveAttribute('data-terminal-status', String(TerminalStatus.EXITED))
      await waitForTerminalText(page, marker)
      const text = await getTerminalText(page)
      expect((text.match(/Worker disconnected - Press Enter to restart/g) ?? []).length).toBe(1)

      expect(await sendActiveTerminalInput(page, '\r')).toBe(true)
      await waitForTerminalReady(page)
      expect(await getTerminalText(page)).toContain(marker)
    }, () => ensureWorkerOnline(separateHubWorker))
  })

  test('reports a native shell exit while the Worker remains online', async ({ separateHubWorker, page, authenticatedWorkspace }) => {
    test.skip(process.platform === 'win32', 'POSIX process signals are unavailable on Windows')
    void authenticatedWorkspace
    await openTerminalViaUI(page)
    await terminalTabs(page).click()
    await waitForTerminalReady(page)
    // SIGKILL cannot become a shell-defined exit code through a signal handler.
    expect(await sendActiveTerminalInput(page, 'kill -KILL $$\r')).toBe(true)
    await waitForTerminalText(page, 'Terminal process exited (-1) - Press Enter to restart')
    await expect(terminalTabs(page)).toHaveAttribute('data-terminal-status', String(TerminalStatus.EXITED))
    expect(await getTerminalText(page)).not.toContain('Worker disconnected')
    expect(await listOnlineWorkerIDsViaAPI(separateHubWorker.hubUrl, separateHubWorker.adminToken)).toContain(separateHubWorker.workerId)
  })

  test('should mark terminal as disconnected when worker stops', async ({ separateHubWorker, page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    // The test stops the worker-scoped Worker and leaves it stopped. The cleanup
    // brings it back, so the delete of the test workspace and a later test of
    // this Playwright worker reach a live Worker.
    await withCleanup(async () => {
      // Open a terminal tab and make sure that it is the active one
      await openTerminalViaUI(page)
      await terminalTabs(page).click()

      // Wait for the terminal to be genuinely ready -- a shell prompt in the
      // xterm buffer -- not a flat 2s. Stopping the worker while the terminal
      // session is still being set up tears it down before it has a buffer to
      // write the disconnect notice into, and 2s is not enough on a box running
      // eight of these at once.
      await expect(async () => {
        expect(await getTerminalText(page)).toMatch(/[$%#>]\s*$/)
      }).toPass()

      // Stop the worker
      await stopWorker(separateHubWorker)

      // Verify the Hub reports this worker as offline. The check reads the
      // worker by its ID, not by its place in the list.
      await waitForWorkerOffline(separateHubWorker)

      // Wait for the exit notice to appear in xterm buffer. Worker
      // shutdown forcibly tears down children before reaping their exit
      // codes, so the notice carries the "Worker disconnected" wording
      // (no exit code) rather than a literal "?" — the worker knows it
      // killed the child, so the cause isn't actually unknown.
      await waitForTerminalText(page, 'Worker disconnected - Press Enter to restart')
    }, () => ensureWorkerOnline(separateHubWorker))
  })

  // A Hub outage keeps the Worker and its shell alive, so the size must reach the
  // same PTY when the Worker comes back. The terminal drops the resize while its
  // Worker is offline, and the ResizeObserver stays quiet after the reconnect,
  // because the dimensions of the view no longer change.
  test('should send a resize from an outage to the shell that survived it', async ({ separateHubWorker, page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openTerminalViaUI(page)
    await terminalTabs(page).click()
    await expect(async () => {
      expect(await getTerminalText(page)).toMatch(/[$%#>]\s*$/)
    }).toPass()
    const rowsBefore = await getTerminalRows(page)
    expect(rowsBefore).toBeGreaterThan(0)
    const viewport = page.viewportSize()
    if (viewport === null)
      throw new Error('this test needs a fixed viewport to shrink')

    await stopHub(separateHubWorker)
    await withCleanup(async () => {
      await expect(terminalTabs(page)).toHaveAttribute('data-terminal-status', TERMINAL_DISCONNECTED)
      await page.setViewportSize({ width: viewport.width, height: viewport.height - 200 })
      await expect.poll(() => getTerminalRows(page)).toBeLessThan(rowsBefore)
    }, () => restartHub(separateHubWorker))

    const rowsDuringTheOutage = await getTerminalRows(page)
    await expect(terminalTabs(page)).not.toHaveAttribute('data-terminal-status', TERMINAL_DISCONNECTED)
    await expect(async () => {
      expect(await sendActiveTerminalInput(page, 'echo SIZE=$(stty size)\r')).toBe(true)
      expect(await getTerminalText(page)).toMatch(new RegExp(`SIZE=${rowsDuringTheOutage} \\d+`))
    }).toPass()
  })

  test('should restart the shell when the user presses Enter on an exited terminal', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openTerminalViaUI(page)
    await terminalTabs(page).click()

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
    await waitForTerminalText(page, 'Terminal process exited (0) - Press Enter to restart')

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
  })
})
