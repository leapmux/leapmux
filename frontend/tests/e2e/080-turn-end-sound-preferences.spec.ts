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

/** The scope chip on the turn-end sound row (dual: browser override vs account). */
function turnEndSoundScope(page: import('@playwright/test').Page) {
  return page.getByTestId('scope-chip-notifications.turnEndSound')
}

/** Switch the turn-end sound row onto the browser (override) tier. */
async function overrideOnDevice(page: import('@playwright/test').Page) {
  await turnEndSoundScope(page).click()
  await page.getByRole('menuitemradio', { name: 'Override on this device' }).click()
}

test.describe('Turn End Sound Preferences', () => {
  test('should show the Turn End Sound row in the Notifications category', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'notifications')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()
    await expect(dialog.getByRole('radio', { name: 'None' })).toBeVisible()
    await expect(dialog.getByRole('radio', { name: 'Ding Dong' })).toBeVisible()
  })

  test('should persist browser-level turn end sound in localStorage', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'notifications')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()

    // The dual row edits whichever tier the scope chip selects; persisting to
    // localStorage means switching to the this-device override first.
    await overrideOnDevice(page)

    // Click "Ding Dong"
    await dialog.getByRole('radio', { name: 'Ding Dong' }).click()
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBe('ding-dong')

    // Click "None"
    await dialog.getByRole('radio', { name: 'None' }).click()
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBe('none')

    // Back to the account tier: the chip's "Use account default" deletes the
    // stored override rather than writing an account-value copy of it.
    await turnEndSoundScope(page).click()
    await page.getByRole('menuitemradio', { name: 'Use account default' }).click()
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBeNull()
  })

  test('should persist account-level turn end sound via API', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/')
    await openPreferencesDialog(page, 'notifications')
    const dialog = page.getByRole('dialog', { name: 'Preferences' })
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()
    // Default scope: the row edits the ACCOUNT tier (the chip reads
    // "Account default" until an override exists).
    await expect(turnEndSoundScope(page)).toHaveText(/Account default/)

    // Select "Ding Dong" and wait for the choice to be reflected before reloading:
    // the write is an API round trip, and reloading mid-flight would race it.
    // role=radio, not button: these pill groups are one-of-N, so they carry
    // radiogroup/radio semantics and aria-checked rather than aria-pressed.
    const dingDong = dialog.getByRole('radio', { name: 'Ding Dong' })
    await dingDong.click()
    await expect(dingDong).toBeChecked()

    // Reload and verify the account-level choice survived the round trip.
    await page.reload()
    await openPreferencesDialog(page, 'notifications')
    const reopened = page.getByRole('dialog', { name: 'Preferences' })
    await expect(reopened.getByText('Turn-end sound', { exact: true })).toBeVisible()
    await expect(reopened.getByRole('radio', { name: 'Ding Dong' })).toBeChecked()

    // Restore to "None" so the account default cannot leak into a later test
    // on this worker's shared hub instance.
    const none = reopened.getByRole('radio', { name: 'None' })
    await none.click()
    await expect(none).toBeChecked()
  })

  test('should play ding-dong sound when turn ends', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)

    await expectDoorbellCount(page, 1)
  })

  test('should NOT play sound when turn end sound is none', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'none')
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

  test('should NOT play sound when opening and closing Preferences dialog', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Open and close the Preferences dialog (no full navigation)
    await openPreferencesDialog(page)
    await page.getByRole('dialog', { name: 'Preferences' }).getByLabel('Close').click()
    await expect(page.getByRole('dialog', { name: 'Preferences' })).not.toBeVisible()

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when closing an agent tab', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)
    await waitForAgentIdle(page)

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

  test('should NOT play sound when opening a new tab', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendMessage(page, TOOL_USING_PROMPT)
    await expectDoorbellCount(page, 1)

    // Opening a new agent tab revises the WatchEvents interest set (no stream
    // restart). A catch-up replay must not be mistaken for a live turn end.
    await openAgentViaUI(page)

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when switching between agent tabs', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
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

  test('should play sound when a turn ends on a tab that is not visible', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await openAgentViaUI(page)
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTabs).toHaveCount(2)

    await agentTabs.first().click()
    await sendMessage(page, TOOL_USING_PROMPT)
    // Hide the working agent before the turn ends — NOTIFY must still ring.
    await agentTabs.nth(1).click()
    await expect(agentTabs.nth(1)).toHaveAttribute('aria-selected', 'true')

    await expectDoorbellCount(page, 1)
    await expect(agentTabs.first().locator('[data-testid="tab-notification"]')).toBeVisible()
  })
})
