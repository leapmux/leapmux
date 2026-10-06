import { withCleanup } from './helpers/cleanup'
import { getTerminalText, sendActiveTerminalInput, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, terminalTabs } from './helpers/ui'
import { ensureWorkerOnline, expect, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test.describe('Terminal Disconnection', () => {
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
