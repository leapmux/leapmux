import { expect, test } from './fixtures'
import { armTurnEndSound, expectDoorbellCount, expectDoorbellQuiet } from './helpers/turnEndSound'
import { getBrowserPref, loginViaToken, openAgentViaUI, openPreferencesDialog, sendMessage, waitForAgentIdle, waitForWorkspaceReady } from './helpers/ui'

/**
 * A prompt the agent cannot answer from the prompt alone, so the turn reports
 * `numToolUses > 0`. That matters: `useTurnEnd` deliberately suppresses the
 * ding for trivial single-exchange turns, so a plain arithmetic question would
 * make every assertion below pass for the wrong reason.
 */
const TOOL_USING_PROMPT = 'Run the command `pwd` and tell me the result.'

test.describe('Turn End Sound Preferences', () => {
  test('should show Turn End Sound section in This Browser tab', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Turn End Sound' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'None' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Ding Dong' }).first()).toBeVisible()
  })

  test('should persist browser-level turn end sound in localStorage', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    await expect(page.getByRole('heading', { name: 'Turn End Sound' }).first()).toBeVisible()

    // Click "Ding Dong"
    await page.getByRole('button', { name: 'Ding Dong' }).first().click()
    await expect.poll(() => getBrowserPref(page, 'turnEndSound')).toBe('ding-dong')

    // Click "None"
    await page.getByRole('button', { name: 'None' }).first().click()
    await expect.poll(() => getBrowserPref(page, 'turnEndSound')).toBe('none')

    // Click "Use account default" within the Turn End Sound section.
    // In the browser tab, "Use account default" buttons appear for: Theme, Terminal Theme,
    // Diff View, Turn End Sound. The Turn End Sound one is the 4th (0-indexed: 3).
    await page.getByRole('button', { name: 'Use account default' }).nth(3).click()
    await expect.poll(() => getBrowserPref(page, 'turnEndSound')).toBeNull()
  })

  test('should show Turn End Sound section in Account Defaults tab', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'None' }).first()).toBeVisible()
    await expect(page.getByRole('button', { name: 'Ding Dong' }).first()).toBeVisible()
  })

  test('should persist account-level turn end sound via API', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page)
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()

    // Select "Ding Dong" and wait for the choice to be reflected before reloading:
    // the write is an API round trip, and reloading mid-flight would race it.
    // role=radio, not button: these pill groups are one-of-N, so they carry
    // radiogroup/radio semantics and aria-checked rather than aria-pressed.
    const dingDong = page.getByRole('radio', { name: 'Ding Dong' }).first()
    await dingDong.click()
    await expect(dingDong).toBeChecked()

    // Reload and verify the account-level choice survived the round trip.
    await page.reload()
    await openPreferencesDialog(page)
    await page.getByRole('tab', { name: 'Account Defaults' }).click()
    await expect(page.getByRole('heading', { name: 'Turn End Sound' })).toBeVisible()
    await expect(page.getByRole('radio', { name: 'Ding Dong' }).first()).toBeChecked()

    // Restore to "None" so the account default cannot leak into a later test
    // on this worker's shared hub instance.
    const none = page.getByRole('radio', { name: 'None' }).first()
    await none.click()
    await expect(none).toBeChecked()
  })

  test('should play ding-dong sound when turn ends', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)

    await expectDoorbellCount(page, 1)
  })

  test('should NOT play sound when turn end sound is none', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'none')
    await waitForWorkspaceReady(page)

    // The SAME tool-using prompt the positive test uses. An arithmetic
    // question would be suppressed by the numToolUses === 0 guard whatever the
    // preference said, so this would pass with the preference plumbing removed
    // entirely -- which is exactly what it did while `setInitialBrowserPref`
    // was silently writing an entry the app discarded.
    await sendMessage(page, TOOL_USING_PROMPT)
    await waitForAgentIdle(page)

    await expectDoorbellQuiet(page, 0)
  })

  test('should NOT play sound when opening and closing Preferences dialog', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Open and close the Preferences dialog (no full navigation)
    await openPreferencesDialog(page)
    await page.getByRole('dialog', { name: 'Preferences' }).getByLabel('Close').click()
    await expect(page.getByRole('dialog', { name: 'Preferences' })).not.toBeVisible()

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when closing an agent tab', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Open a second agent tab so we have somewhere to land after closing
    await openAgentViaUI(page)

    // Switch back to the first agent tab (the one with a completed turn)
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()
    await expect(agentTabs.first()).toHaveAttribute('aria-selected', 'true')

    // Close it. Closing a tab whose turn already ended must not re-ring.
    await agentTabs.first().locator('[data-testid="tab-close"]').click()
    await expect(agentTabs).toHaveCount(1)

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when opening a new tab', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Opening a new agent tab restarts the WatchEvents stream, which replays
    // the completed turn. The replay must not be mistaken for a live turn end.
    await openAgentViaUI(page)

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when switching between agent tabs', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Open a second agent tab, then switch back and forth
    await openAgentViaUI(page)
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()
    await expect(agentTabs.first()).toHaveAttribute('aria-selected', 'true')
    await agentTabs.nth(1).click()
    await expect(agentTabs.nth(1)).toHaveAttribute('aria-selected', 'true')

    await expectDoorbellQuiet(page, 1)
  })
})
