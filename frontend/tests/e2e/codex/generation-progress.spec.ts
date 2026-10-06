import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { exerciseOutputByteProgress } from '../helpers/generationProgress'
import { toolRows } from '../helpers/ui'
import { waitForCodexCommandStart } from './commandStart'

const OUTPUT_MARKER = 'LEAPMUX OUTPUT two'

codexTest.describe('generation progress', () => {
  codexTest('shows process bytes without rendering partial process output', async ({ native }) => {
    const page = native.page
    await exerciseOutputByteProgress(native, {
      supported: true,
      outputMarkers: { first: OUTPUT_MARKER, second: 'LEAPMUX OUTPUT eight' },
      // Codex streams only the output that a command writes after Codex attached to
      // it. The command writes its first segment after the Worker stored the start.
      waitForToolStart: () => waitForCodexCommandStart(native),
      afterOutputBoundary: async ({ firstMarker, secondMarker, count }) => {
        expect(count).toBeGreaterThan(0)
        await expect(toolRows(page).filter({ hasText: firstMarker })).toHaveCount(0)
        await expect(toolRows(page).filter({ hasText: secondMarker })).toHaveCount(0)
      },
    })
    await expect(toolRows(page).filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(toolRows(page).filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
  })
})
