import { expect } from '@playwright/test'
import { test } from './fixtures'
import { waitForSettingsHydrated } from './helpers/ui'

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
})
