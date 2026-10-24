import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { exerciseOutputByteProgress, exerciseTokenProgress } from '../helpers/generationProgress'

droidTest('shows an advancing estimated token count during native generation', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})

droidTest('shows an advancing minimum byte count during native tool output', async ({ native }) => {
  await exerciseOutputByteProgress(native, {
    supported: true,
    afterOutputBoundary: async () => {
      const count = native.page.locator('[data-testid="thinking-output-count"]:visible')
      await expect(count).toContainText('≥')
      await count.hover()
      await expect(native.page.getByRole('tooltip').filter({ hasText: 'The provider limits live output. This count is a minimum.' })).toBeVisible()
      await native.page.mouse.move(0, 0)
    },
  })
})
