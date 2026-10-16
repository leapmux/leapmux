import type { Page } from '@playwright/test'
import type { ServerInfo } from './fixtures'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { currentNativeAgent, nativeOptionGroup } from './helpers/nativeScenario'
import { agentTabs, chooseSettingsOption, composerEditor, expectSettingsChip, messageBubbles, openAgentViaUI, settingsBar, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'

/** Select a real alternative from the current Worker catalog without a provider-specific value. */
async function settingChoice(page: Page, leapmuxServer: ServerInfo, groupId: string) {
  const group = nativeOptionGroup(await currentNativeAgent({ page, leapmuxServer }), groupId)
  const current = group?.options.find(option => option.id === group.currentValue)
  const alternative = group?.options.find(option => option.id !== group.currentValue)
  if (!group?.mutable || !current || !alternative)
    throw new Error(`The settings test requires a mutable ${groupId} group with a current value and an alternative.`)
  return { groupId, current, alternative }
}

test.describe('agent settings', () => {
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

  test('allows editor focus after a mode change', async ({ authenticatedWorkspace, page, leapmuxServer }) => {
    void authenticatedWorkspace
    await waitForSettingsHydrated(page)
    const editor = composerEditor(page)
    await expect(editor).toBeVisible()
    const choice = await settingChoice(page, leapmuxServer, 'permissionMode')
    await chooseSettingsOption(page, `${choice.groupId}-${choice.alternative.id}`)
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('composer-plus-popover')).not.toBeVisible()
    await editor.click()
    await expect(editor).toBeFocused()
  })

  test('permission mode change in new agent tab targets correct agent', async ({ authenticatedWorkspace, page, leapmuxServer }) => {
    void authenticatedWorkspace
    await waitForSettingsHydrated(page)
    const first = await settingChoice(page, leapmuxServer, 'permissionMode')
    await expectSettingsChip(page, first.current.name)
    await openAgentViaUI(page)
    await waitForSettingsHydrated(page)
    const second = await settingChoice(page, leapmuxServer, 'permissionMode')
    await expectSettingsChip(page, second.current.name)
    await chooseSettingsOption(page, `${second.groupId}-${second.alternative.id}`)
    await expectSettingsChip(page, second.alternative.name)
    await waitForSettingsIdle(page)
    const notice = `${second.current.name} → ${second.alternative.name}`
    await expect(messageBubbles(page).filter({ hasText: notice })).toBeVisible()
    await agentTabs(page).first().click()
    await expectSettingsChip(page, first.current.name)
    await expect(messageBubbles(page).filter({ hasText: notice })).toHaveCount(0)
  })

  test('settings loading indicator in the status bar', async ({ authenticatedWorkspace, page, leapmuxServer }) => {
    void authenticatedWorkspace
    await expect(settingsBar(page)).toBeVisible()
    const choice = await settingChoice(page, leapmuxServer, 'model')
    await chooseSettingsOption(page, `${choice.groupId}-${choice.alternative.id}`)
    const spinner = page.getByTestId('settings-loading-spinner')
    await expect(spinner).toBeVisible()
    await expect(spinner).not.toBeVisible()
    // A default-model sentinel can resolve to a concrete model. Compare the chip with the confirmed Worker value.
    const confirmed = await settingChoice(page, leapmuxServer, 'model')
    await expectSettingsChip(page, confirmed.current.name)
  })
})
