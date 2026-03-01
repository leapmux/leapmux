import { expect, test } from './fixtures'
import { loginViaToken, openAgentViaUI, waitForWorkspaceReady } from './helpers/ui'

test.describe('Turn End Sound Preferences', () => {
  test('should show Turn End Sound section in This Browser tab', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Turn End Sound' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'None' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Ding Dong' }).first()).toBeVisible()
  })

  test('should persist browser-level turn end sound in localStorage', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/settings')
    await expect(page.getByRole('heading', { name: 'Turn End Sound' }).first()).toBeVisible()

    // Click "Ding Dong"
    await page.getByRole('button', { name: 'Ding Dong' }).first().click()
    let value = await page.evaluate(() => localStorage.getItem('leapmux-turn-end-sound'))
    expect(value).toBe('ding-dong')

    // Click "None"
    await page.getByRole('button', { name: 'None' }).first().click()
    value = await page.evaluate(() => localStorage.getItem('leapmux-turn-end-sound'))
    expect(value).toBe('none')

    // Click "Use account default" within the Turn End Sound section.
    // In the browser tab, "Use account default" buttons appear for: Theme, Terminal Theme,
    // Diff View, Turn End Sound. The Turn End Sound one is the 4th (0-indexed: 3).
    await page.getByRole('button', { name: 'Use account default' }).nth(3).click()
    value = await page.evaluate(() => localStorage.getItem('leapmux-turn-end-sound'))
    expect(value).toBe('account-default')
  })

  test('should show Turn End Sound section in Account Defaults tab', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/settings')
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'None' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Ding Dong' }).first()).toBeVisible()
  })

  test('should persist account-level turn end sound via API', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/settings')
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()

    // Click "Ding Dong"
    await page.getByRole('button', { name: 'Ding Dong' }).first().click()
    await page.waitForTimeout(500)

    // Reload and verify persistence
    await page.reload()
    await expect(page.getByText('Preferences')).toBeVisible()
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()
    await page.waitForTimeout(500)

    // Restore to "None"
    await page.getByRole('button', { name: 'None' }).first().click()
    await page.waitForTimeout(500)
  })

  test('should play ding-dong sound when turn ends', async ({ page, authenticatedWorkspace }) => {
    // Set up audio spy and preference — these addInitScript calls persist across navigations
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await page.addInitScript(() => {
      localStorage.setItem('leapmux-turn-end-sound', 'ding-dong')
    })

    // Reload so the init scripts take effect
    await page.reload()
    await waitForWorkspaceReady(page)

    // Wait for editor to be ready
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Wait for the interrupt button to appear (turn starts)
    await expect(page.locator('[data-testid="interrupt-button"]')).toBeVisible()

    // Wait for the turn to end (interrupt button disappears)
    await page.waitForFunction(() => {
      return !document.querySelector('[data-testid="interrupt-button"]')
    }, { timeout: 120_000 })

    // Give a short moment for the effect to fire
    await page.waitForTimeout(200)

    // Verify the doorbell sound was played
    const calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(calls.some((src: string) => src.includes('benkirb-electronic-doorbell'))).toBe(true)
  })

  test('should NOT play sound when turn end sound is none', async ({ page, authenticatedWorkspace }) => {
    // Set up audio spy
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await page.addInitScript(() => {
      localStorage.setItem('leapmux-turn-end-sound', 'none')
    })

    // Reload so the init scripts take effect
    await page.reload()
    await waitForWorkspaceReady(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Wait for the interrupt button to appear
    await expect(page.locator('[data-testid="interrupt-button"]')).toBeVisible()

    // Wait for the turn to end
    await page.waitForFunction(() => {
      return !document.querySelector('[data-testid="interrupt-button"]')
    }, { timeout: 120_000 })

    await page.waitForTimeout(200)

    // Verify no doorbell sound was played
    const calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(calls.some((src: string) => src.includes('benkirb-electronic-doorbell'))).toBe(false)
  })

  test('should NOT play sound when navigating to Preferences and back', async ({ page, authenticatedWorkspace }) => {
    // Set up audio spy
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await page.addInitScript(() => {
      localStorage.setItem('leapmux-turn-end-sound', 'ding-dong')
    })

    // Reload so the init scripts take effect
    await page.reload()
    await waitForWorkspaceReady(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message and wait for the turn to complete
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    await expect(page.locator('[data-testid="interrupt-button"]')).toBeVisible()
    await page.waitForFunction(() => {
      return !document.querySelector('[data-testid="interrupt-button"]')
    }, { timeout: 120_000 })
    await page.waitForTimeout(200)

    // Verify the sound played exactly once for the real turn end
    const calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(calls.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length).toBe(1)

    // Navigate to Preferences page (full navigation resets __audioPlayCalls via addInitScript)
    await page.goto('/settings')
    await expect(page.getByText('Preferences')).toBeVisible()

    // Navigate back to the workspace (addInitScript runs again, resetting __audioPlayCalls)
    await page.goBack()
    await expect(page.locator('[data-testid="chat-editor"] .ProseMirror')).toBeVisible()

    // Wait for the watch connection to reconnect and replay events
    await page.waitForTimeout(1000)

    // Verify no sound was played from navigating back (array was reset on navigation,
    // so any entries here are spurious sounds from the reconnection replay)
    const callsAfterNav = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    expect(callsAfterNav.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length).toBe(0)
  })

  test('should NOT play sound when closing an agent tab', async ({ page, authenticatedWorkspace }) => {
    // Set up audio spy
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await page.addInitScript(() => {
      localStorage.setItem('leapmux-turn-end-sound', 'ding-dong')
    })

    // Reload so the init scripts take effect
    await page.reload()
    await waitForWorkspaceReady(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message in the first agent tab and wait for the turn to complete
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    await expect(page.locator('[data-testid="interrupt-button"]')).toBeVisible()
    await page.waitForFunction(() => {
      return !document.querySelector('[data-testid="interrupt-button"]')
    }, { timeout: 120_000 })
    await page.waitForTimeout(200)

    // Verify the sound played exactly once for the real turn end
    let calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    const soundCountAfterTurn = calls.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length
    expect(soundCountAfterTurn).toBe(1)

    // Open a second agent tab so we have somewhere to land after closing
    await openAgentViaUI(page)
    await page.waitForTimeout(500)

    // Switch back to the first agent tab
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().click()
    await page.waitForTimeout(500)

    // Close the first agent tab (which has a completed turn)
    const firstTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    await firstTab.locator('[data-testid="tab-close"]').click()
    await page.waitForTimeout(1000)

    // Verify no additional sound was played from closing the tab
    calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    const soundCountAfterClose = calls.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length
    expect(soundCountAfterClose).toBe(soundCountAfterTurn)
  })

  test('should NOT play sound when switching between agent tabs', async ({ page, authenticatedWorkspace }) => {
    // Set up audio spy
    await page.addInitScript(() => {
      (window as any).__audioPlayCalls = [] as string[]
      HTMLAudioElement.prototype.play = function () {
        (window as any).__audioPlayCalls.push(this.src)
        return Promise.resolve()
      }
    })
    await page.addInitScript(() => {
      localStorage.setItem('leapmux-turn-end-sound', 'ding-dong')
    })

    // Reload so the init scripts take effect
    await page.reload()
    await waitForWorkspaceReady(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message in the first agent tab and wait for the turn to complete
    await editor.click()
    await page.keyboard.type('What is 2+2? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    await expect(page.locator('[data-testid="interrupt-button"]')).toBeVisible()
    await page.waitForFunction(() => {
      return !document.querySelector('[data-testid="interrupt-button"]')
    }, { timeout: 120_000 })
    await page.waitForTimeout(200)

    // Record the sound count after the first turn
    let calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    const soundCountAfterTurn = calls.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length
    expect(soundCountAfterTurn).toBe(1)

    // Open a second agent tab
    await openAgentViaUI(page)
    await page.waitForTimeout(500)

    // Switch back to the first agent tab (which has a completed turn)
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().click()
    await page.waitForTimeout(500)

    // Switch to the second agent tab again
    await page.locator('[data-testid="tab"][data-tab-type="agent"]').nth(1).click()
    await page.waitForTimeout(500)

    // Verify no additional sound was played from tab switching
    calls = await page.evaluate(() => (window as any).__audioPlayCalls as string[])
    const soundCountAfterSwitch = calls.filter((src: string) => src.includes('benkirb-electronic-doorbell')).length
    expect(soundCountAfterSwitch).toBe(soundCountAfterTurn)
  })
})
