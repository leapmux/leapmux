import type { Page } from '@playwright/test'
import type { ModelScript } from './helpers/modelScriptFixture'
import { expect, test } from './fixtures'
import { nativeAgentById } from './helpers/nativeScenario'
import { openPreferencesAs, preferenceScopeChip, setPreferenceScope } from './helpers/preferences'
import { retryUntilPass } from './helpers/retryUntilPass'
import { armTurnEndSound, expectDoorbellCount, expectDoorbellQuiet, sendToolUsingTurn, waitForIdleSoundReceipt } from './helpers/turnEndSound'
import { agentTabs, expectAgentTabCount, getBrowserPref, openAgentViaUI, openSettingsAt, tabById, waitForAgentIdle, waitForWorkspaceReady } from './helpers/ui'

// The doorbell fires on a turn that USED a tool, so each turn here makes a real
// tool call through `sendToolUsingTurn` -- a text-only answer is the negative
// case, and the spec has its own test for that.

/** Expire the native sound handler's sixty-second cooldown before a UI-only negative case. */
async function prepareUiSoundProbe(page: Page, userId: string, script: ModelScript): Promise<void> {
  await page.clock.install()
  await armTurnEndSound(page, userId, 'ding-dong')
  await waitForWorkspaceReady(page)
  const boundary = await sendToolUsingTurn(page, script)
  await waitForIdleSoundReceipt(page, boundary)
  await expectDoorbellCount(page, 1)
  await page.clock.fastForward(61_000)
}

/** The setting ID of the turn-end sound row (dual: browser override vs account). */
const TURN_END_SOUND = 'notifications.turnEndSound'

test.describe('Turn End Sound Preferences', () => {
  test('should show the Turn End Sound row in the Notifications category', async ({ page, leapmuxServer }) => {
    const dialog = await openPreferencesAs(page, leapmuxServer.adminToken, 'notifications')
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()
    await expect(dialog.getByRole('radio', { name: 'None' })).toBeVisible()
    await expect(dialog.getByRole('radio', { name: 'Ding Dong' })).toBeVisible()
  })

  test('should persist browser-level turn end sound in browser storage', async ({ page, leapmuxServer }) => {
    const dialog = await openPreferencesAs(page, leapmuxServer.adminToken, 'notifications')
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()

    // The dual row edits whichever tier the scope chip selects; persisting to
    // browser storage means switching to the this-device override first.
    await setPreferenceScope(page, TURN_END_SOUND, 'device')

    // Click "Ding Dong"
    await dialog.getByRole('radio', { name: 'Ding Dong' }).click()
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBe('ding-dong')

    // Click "None"
    await dialog.getByRole('radio', { name: 'None' }).click()
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBe('none')

    // Back to the account tier: the chip's "Use account default" deletes the
    // stored override rather than writing an account-value copy of it.
    await setPreferenceScope(page, TURN_END_SOUND, 'account')
    await expect.poll(() => getBrowserPref(page, leapmuxServer.adminUserId, 'turnEndSound')).toBeNull()
  })

  test('should persist account-level turn end sound via API', async ({ page, leapmuxServer }) => {
    const dialog = await openPreferencesAs(page, leapmuxServer.adminToken, 'notifications')
    await expect(dialog.getByText('Turn-end sound', { exact: true })).toBeVisible()
    // Default scope: the row edits the ACCOUNT tier (the chip reads
    // "Account default" until an override exists).
    await expect(preferenceScopeChip(page, TURN_END_SOUND)).toHaveText(/Account default/)

    // Select "Ding Dong" and wait for the choice to be reflected before reloading:
    // the write is an API round trip, and reloading mid-flight would race it.
    // role=radio, not button: these pill groups are one-of-N, so they carry
    // radiogroup/radio semantics and aria-checked rather than aria-pressed.
    const dingDong = dialog.getByRole('radio', { name: 'Ding Dong' })
    await dingDong.click()
    await expect(dingDong).toBeChecked()

    // Reload and verify the account-level choice survived the round trip.
    await page.reload()
    const reopened = await openSettingsAt(page, 'notifications')
    await expect(reopened.getByText('Turn-end sound', { exact: true })).toBeVisible()
    await expect(reopened.getByRole('radio', { name: 'Ding Dong' })).toBeChecked()
    // The suite reset restores the account setting before the next test.
  })

  test('should play ding-dong sound when turn ends', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await sendToolUsingTurn(page, modelScript)

    await expectDoorbellCount(page, 1)
  })

  test('should NOT play sound when turn end sound is none', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'none')
    await waitForWorkspaceReady(page)

    // The SAME tool-using prompt the positive test uses. An arithmetic
    // question would be suppressed by the numToolUses === 0 guard whatever the
    // preference said, so this would pass with the preference plumbing removed
    // entirely -- which is exactly what it did while `setInitialBrowserPref`
    // was silently writing an entry the app discarded.
    const boundary = await sendToolUsingTurn(page, modelScript)
    await waitForAgentIdle(page)
    await expectDoorbellQuiet(page, 0, boundary)
  })

  test('should NOT play sound when opening and closing Preferences dialog', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    await prepareUiSoundProbe(page, leapmuxServer.adminUserId, modelScript)

    // Open and close the Preferences dialog (no full navigation)
    const dialog = await openSettingsAt(page)
    await dialog.getByLabel('Close').click()
    await expect(dialog).not.toBeVisible()

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when closing an agent tab', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    await prepareUiSoundProbe(page, leapmuxServer.adminUserId, modelScript)

    // Open a second agent tab so we have somewhere to land after closing
    await openAgentViaUI(page)

    // Switch back to the agent of the fixture, the one with a completed turn.
    const closingId = authenticatedWorkspace.agentId
    const closing = tabById(page, closingId)
    await closing.click()
    await expect(closing).toHaveAttribute('aria-selected', 'true')

    // Close it. Closing a tab whose turn already ended must not re-ring.
    await closing.locator('[data-testid="tab-close"]').click()
    await expectAgentTabCount(page, 1)
    await retryUntilPass(async () => {
      expect(await nativeAgentById({ leapmuxServer }, closingId), 'the Worker lists the closed agent no more').toBeNull()
    })

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when opening a new tab', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    await prepareUiSoundProbe(page, leapmuxServer.adminUserId, modelScript)

    // Opening a new agent tab revises the WatchEvents interest set (no stream
    // restart). A catch-up replay must not be mistaken for a live turn end.
    await openAgentViaUI(page)

    await expectDoorbellQuiet(page, 1)
  })

  test('should NOT play sound when switching between agent tabs', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace // fixture trigger
    await prepareUiSoundProbe(page, leapmuxServer.adminUserId, modelScript)

    // Open a second agent tab, then switch back and forth
    await openAgentViaUI(page)
    const tabs = agentTabs(page)
    await tabs.first().click()
    await expect(tabs.first()).toHaveAttribute('aria-selected', 'true')
    await tabs.nth(1).click()
    await expect(tabs.nth(1)).toHaveAttribute('aria-selected', 'true')

    await expectDoorbellQuiet(page, 1)
  })

  test('should play sound when a turn ends on a tab that is not visible', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    void authenticatedWorkspace
    await armTurnEndSound(page, leapmuxServer.adminUserId, 'ding-dong')
    await waitForWorkspaceReady(page)

    await openAgentViaUI(page)
    const tabs = agentTabs(page)
    await expectAgentTabCount(page, 2)

    await tabs.first().click()
    await sendToolUsingTurn(page, modelScript)
    // Hide the working agent before the turn ends — NOTIFY must still ring.
    await tabs.nth(1).click()
    await expect(tabs.nth(1)).toHaveAttribute('aria-selected', 'true')

    await expectDoorbellCount(page, 1)
    await expect(tabs.first().locator('[data-testid="tab-notification"]')).toBeVisible()
  })
})
