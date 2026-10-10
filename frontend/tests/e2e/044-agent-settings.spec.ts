import { expect } from '@playwright/test'
import { test } from './fixtures'
import { agentTabs, chooseSettingsOption, composerEditor, expectSettingsChip, openAgentViaUI, permissionModeOffered, settingsBar, visibleOnly, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'

test.describe('Agent Settings', () => {
  // This fixture has no Git repository, so it has no branch chip.
  // The worktree spec checks that chip with an actual branch.
  test('keeps descenders visible inside composer chip labels', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    await waitForSettingsHydrated(page)

    await expect(page.getByTestId('composer-model-trigger')).toBeVisible()

    const chips = page.locator(
      '[data-testid="composer-status-bar"] button[data-testid^="composer-"][data-testid$="-trigger"]:visible',
    )
    const clippedLabels = await chips.evaluateAll((buttons) => {
      return buttons.flatMap((button) => {
        const label = Array.from(button.children).find(child => child instanceof HTMLSpanElement)
        const chipId = button.getAttribute('data-testid') ?? 'unknown composer chip'
        if (!label)
          return [`${chipId}: no direct label span`]

        // Use every Latin descender. The label must leave space for all four.
        // A real provider value or branch can contain none of them.
        label.textContent = 'gypq'
        const labelRect = label.getBoundingClientRect()
        const range = document.createRange()
        range.selectNodeContents(label)
        const textRect = range.getBoundingClientRect()
        const overflowY = getComputedStyle(label).overflowY
        const textExceedsLabel = textRect.top < labelRect.top - 0.25
          || textRect.bottom > labelRect.bottom + 0.25

        return textExceedsLabel && overflowY !== 'visible'
          ? [`${chipId}: text ${textRect.top}-${textRect.bottom}, label ${labelRect.top}-${labelRect.bottom}, overflow-y ${overflowY}`]
          : []
      })
    })

    expect(clippedLabels, 'composer chip labels clip their font ink').toEqual([])
  })

  // The settings UI owns these three behaviors, so they live beside the shared
  // settings checks instead of the default provider's mode spec. Their assertions
  // moved unchanged from that spec.
  test('focus returns to editor after mode change', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    // Require the editor after agent startup.
    const editor = composerEditor(page)
    await expect(editor).toBeVisible()

    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Open dropdown and click a mode
    await chooseSettingsOption(page, 'permissionMode-plan')

    // Close the dropdown by pressing Escape
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="composer-plus-popover"]')).not.toBeVisible()

    // Click the editor and verify it can receive focus
    await editor.click()
    await expect(editor).toBeFocused()
  })

  test('permission mode change in new agent tab targets correct agent', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Verify first agent starts with Default mode
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Default')

    // Open a second agent tab
    await openAgentViaUI(page)

    // A new session requests Auto Mode. The CLI startup probe determines whether that mode is available.
    // Read the picker to determine the exact expected mode. An alternative regular expression can accept an incorrect default.
    await waitForSettingsHydrated(page)
    const expectedMode = await permissionModeOffered(page, 'auto') ? 'Auto Mode' : 'Default'
    await expectSettingsChip(page, expectedMode)

    // Switch the new agent to Plan Mode
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan Mode')
    await waitForSettingsIdle(page)

    // Require the notification in the new agent's chat.
    await expect(visibleOnly(page.getByText(`Mode (${expectedMode} → Plan Mode)`))).toBeVisible()

    // Switch back to the first agent tab
    await agentTabs(page).first().click()

    // First agent should still be in Default mode
    await expectSettingsChip(page, 'Default')
    // And should NOT have the permission mode notification
    await expect(visibleOnly(page.getByText('Mode (Default → Plan Mode)'))).not.toBeVisible()
  })

  test('settings loading indicator in the status bar', async ({ authenticatedWorkspace, page }) => {
    void authenticatedWorkspace
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Select Haiku from the default Sonnet model. The chip changes before the Worker confirms the setting.
    // Require the spinner during that unconfirmed state.
    await chooseSettingsOption(page, 'model-haiku')

    const loadingSpinner = page.locator('[data-testid="settings-loading-spinner"]')
    await expect(loadingSpinner).toBeVisible()

    // Require spinner removal after statusChange arrives.
    await expect(loadingSpinner).not.toBeVisible()
    await expectSettingsChip(page, 'Haiku')
  })
})
