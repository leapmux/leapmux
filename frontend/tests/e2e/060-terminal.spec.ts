import { expect, test } from './fixtures'
import { focusActiveTerminal, getTerminalText, typeInTerminal, waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, waitForLayoutSave } from './helpers/ui'
import { listTerminalsViaAPI } from './helpers/worktree'

test.describe('Terminal', () => {
  test('should open a terminal and render xterm', async ({ page, authenticatedWorkspace }) => {
    // Open a new terminal via the tab bar + button
    await openTerminalViaUI(page)

    // Verify terminal tab appears in the unified tab bar
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    // Verify xterm element is rendered
    await expect(page.locator('.xterm')).toBeVisible()
  })

  test('should preserve terminal content when switching between tabs', async ({ page, authenticatedWorkspace }) => {
    // Open terminal 1 and type a marker
    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()
    await typeInTerminal(page, 'echo TERM1MARKER')
    await waitForTerminalText(page, 'TERM1MARKER')

    // Open terminal 2 and type a marker
    await openTerminalViaUI(page)
    // Wait for the second terminal tab to appear
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]').nth(1)).toBeVisible()
    // Wait for the new terminal to render
    await page.waitForTimeout(500)
    await typeInTerminal(page, 'echo TERM2MARKER')
    await waitForTerminalText(page, 'TERM2MARKER')

    // Switch back to terminal 1 tab (first terminal tab) using data-testid
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').first().click()
    await page.waitForTimeout(1000)

    // Terminal 1 content should be visible -- read from the active container
    await waitForTerminalText(page, 'TERM1MARKER', 30_000)

    // Switch back to terminal 2 tab (second terminal tab)
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').nth(1).click()
    await page.waitForTimeout(1000)
    await waitForTerminalText(page, 'TERM2MARKER', 30_000)
  })

  test('should keep xterm in alt-screen after page refresh once the ring has wrapped', async ({ page, authenticatedWorkspace }) => {
    // Reproduces the rendering bug the modeTracker fix exists for:
    // toggling alt-screen, then emitting >100 KB of output so the
    // toggle falls out of the worker's retained ring, then refreshing
    // the page. The frontend hydrates xterm via terminal.reset() +
    // write(snapshot). Without the tracker prefix, xterm comes up in
    // main screen and renders the alt-screen body bytes as garbage.

    const saved = waitForLayoutSave(page)
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()
    await saved

    // Toggle alt-screen, paint a sentinel, then push ~150 KB of
    // filler. `yes ... | head -c N` is portable across macOS and Linux
    // and emits printable bytes (no null pollution in xterm).
    await typeInTerminal(page, 'printf \'\\033[?1049h\'; yes leapmux-altscreen-filler | head -c 150000; printf \'DONE_FILLING\\n\'')
    await waitForTerminalText(page, 'DONE_FILLING', 30_000)

    const getBufferType = () =>
      page.evaluate(() => (window as any).__getActiveTerminalBufferType?.() ?? 'normal')

    // Sanity: the terminal is currently in alt screen. If this fails,
    // the test setup is wrong and the post-refresh assertion below
    // would be meaningless.
    expect(await getBufferType()).toBe('alternate')

    await page.reload()
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').click()
    await expect(page.locator('.xterm')).toBeVisible()

    // The sentinel itself is gone — it fell out of the ring along with
    // the alt-screen toggle. What we CAN verify is the buffer type:
    // the tracker's snapshotPrefix must have set xterm back into
    // alt-screen mode before replay.
    await expect(async () => {
      expect(await getBufferType()).toBe('alternate')
    }).toPass()
  })

  test('should restore terminal screen content after page refresh', async ({ page, authenticatedWorkspace }) => {
    // Start listening for the layout save before opening the terminal
    const saved = waitForLayoutSave(page)

    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for the layout save to complete so the terminal tab is persisted
    await saved

    // Type a marker and wait for it to appear
    await typeInTerminal(page, 'echo SCREENRESTORE')
    await waitForTerminalText(page, 'SCREENRESTORE')

    // Refresh the page
    await page.reload()

    // Terminal tab should be restored automatically (no mode switch needed)
    // Click on the terminal tab to activate it
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').click()

    // Verify xterm is visible (terminal restored from worker)
    await expect(page.locator('.xterm')).toBeVisible()

    // Verify screen content was restored
    await waitForTerminalText(page, 'SCREENRESTORE')
  })

  test('should terminate shell in worker when terminal tab is closed', async ({ page, authenticatedWorkspace }) => {
    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Close the terminal tab (click the x button on the terminal tab)
    await page.locator('[data-testid="tab"][data-tab-type="terminal"] [data-testid="tab-close"]').first().click()

    // Verify no terminal tabs remain
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()

    // Refresh the page
    await page.reload()

    // Verify no terminal tabs appear (worker killed the shell)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()
  })

  test('should keep terminal tab but stop input after shell exits via "exit"', async ({ page, authenticatedWorkspace }) => {
    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Type "exit" to terminate the shell
    await typeInTerminal(page, 'exit')

    // Wait a moment for the exit notification to arrive
    await page.waitForTimeout(2000)

    // The terminal tab should still be visible (not removed)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    // The xterm should still be visible (shows final output)
    await expect(page.locator('.xterm')).toBeVisible()

    // Verify the terminal no longer accepts input: type something and
    // confirm it does NOT appear in the terminal output
    await focusActiveTerminal(page)
    await page.keyboard.type('echo SHOULD_NOT_APPEAR', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.waitForTimeout(1000)
    const textAfter = await getTerminalText(page)
    expect(textAfter).not.toContain('SHOULD_NOT_APPEAR')

    // Closing the tab manually should work.
    // Use dispatchEvent to avoid Playwright actionability timeout issues
    // when xterm's helper textarea holds focus after the shell exited.
    await page.locator('[data-testid="tab"][data-tab-type="terminal"] [data-testid="tab-close"]').dispatchEvent('click')
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()
  })

  test('should resize terminal to fit panel dimensions', async ({ page, authenticatedWorkspace }) => {
    // Start with a smaller viewport to get a small initial terminal
    await page.setViewportSize({ width: 900, height: 600 })

    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for xterm to initialize with rows
    let initialRows = 0
    await expect(async () => {
      initialRows = await page.evaluate(() => {
        if (typeof (window as any).__getActiveTerminalRows === 'function') {
          return (window as any).__getActiveTerminalRows() as number
        }
        return 0
      })
      expect(initialRows).toBeGreaterThan(0)
    }).toPass()

    // Resize viewport much larger
    await page.setViewportSize({ width: 1600, height: 1000 })

    // Poll until row count increases (ResizeObserver + fit needs time)
    await expect(async () => {
      const newRows = await page.evaluate(() => {
        if (typeof (window as any).__getActiveTerminalRows === 'function') {
          return (window as any).__getActiveTerminalRows() as number
        }
        return 0
      })
      expect(newRows).toBeGreaterThan(initialRows)
    }).toPass()
  })

  test('switching back into a hidden terminal keeps prior content without a blank frame', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()
    await typeInTerminal(page, 'echo KEEP_ON_SWITCH')
    await waitForTerminalText(page, 'KEEP_ON_SWITCH')

    // Cover with the workspace's existing agent tab — demotion must clear nothing.
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    await agentTab.click()
    await expect(agentTab).toHaveAttribute('aria-selected', 'true')

    // Queue output while hidden, then switch back and assert both markers without
    // an intervening empty read (retained buffer + ring catch-up).
    const termTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]').first()
    await termTab.click()
    await typeInTerminal(page, 'sleep 0.5; echo HIDDEN_OUTPUT')
    await agentTab.click()

    await termTab.click()
    // Immediate: retained buffer still has the pre-hide marker (no blank frame).
    expect(await getTerminalText(page)).toContain('KEEP_ON_SWITCH')
    // Shortly: catch-up / live bytes for what ran while hidden.
    await waitForTerminalText(page, 'HIDDEN_OUTPUT')
  })
  /**
   * The sidebar tree's rename used to patch the local metadata and stop there:
   * it called `renameAgent` for an agent and NOTHING for a terminal, so a
   * terminal renamed from the sidebar reached no other client and lost the name
   * on the next reload. Both rename affordances now go through one client-side
   * write point.
   *
   * The title carries characters the rule strips, so this also proves the two
   * sides agree: the tab strip shows the cleaned title straight away, and the
   * worker stores that same title rather than the raw one. The worker RPC is
   * what the poll asks -- the tab label alone is optimistic local state.
   */
  test('a sidebar rename stores the cleaned title on the worker', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    await openTerminalViaUI(page)

    const terminalTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTab).toBeVisible()
    const terminalId = await terminalTab.getAttribute('data-tab-id')
    expect(terminalId).toBeTruthy()

    const leaf = page.locator(`[data-testid="tab-tree-leaf"][data-tab-id="${terminalId}"]:visible`)
    await leaf.dblclick()
    const renameInput = leaf.locator('input')
    await expect(renameInput).toBeVisible()
    await renameInput.fill('Build $watcher "1"')
    await renameInput.press('Enter')

    await expect(terminalTab).toContainText('Build watcher 1')

    const { hubUrl, adminToken, workerId } = leapmuxServer
    await expect.poll(async () => {
      const terminals = await listTerminalsViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
      return terminals.map(t => t.title)
    }, 'the sidebar rename must reach the worker, holding the cleaned title').toContain('Build watcher 1')
  })
})
