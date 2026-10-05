import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { waitForCodexCommandStart } from './commandStart'

const OUTPUT_MARKER = 'LEAPMUX OUTPUT two'

codexTest.describe('generation progress', () => {
  codexTest('shows process bytes without rendering partial process output', async ({ authenticatedCodexWorkspace, page, leapmuxServer, modelScript }) => {
    const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CODEX, workspaceId: authenticatedCodexWorkspace.workspaceId }
    await exerciseGenerationProgress(context, {
      supported: true,
      counter: 'bytes',
      outputMarkers: { first: OUTPUT_MARKER, second: 'LEAPMUX OUTPUT eight' },
      // Codex streams only the output that a command writes after Codex attached to
      // it. The command writes its first segment after the Worker stored the start.
      waitForToolStart: () => waitForCodexCommandStart({ page, leapmuxServer }),
      afterOutputBoundary: async ({ firstMarker, secondMarker, count }) => {
        expect(count).toBeGreaterThan(0)
        await expect(page.locator('[data-tool-message]:visible').filter({ hasText: firstMarker })).toHaveCount(0)
        await expect(page.locator('[data-tool-message]:visible').filter({ hasText: secondMarker })).toHaveCount(0)
      },
    })
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
    await page.reload()
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: OUTPUT_MARKER }).first()).toBeVisible()
  })
})
