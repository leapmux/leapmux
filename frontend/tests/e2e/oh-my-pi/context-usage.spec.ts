import { expect } from '@playwright/test'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The native usage event must reach the agent info card. The test checks the reported count and its display.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('reports model usage in the agent info card', async ({ authenticatedOhMyPiWorkspace, page, modelScript }) => {
    void authenticatedOhMyPiWorkspace
    await modelScript.queue({ text: 'Usage recorded.', usage: { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 } })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    // OMP adds its own token estimate for the post-answer context tail.
    const popover = await openAgentInfoCard(page)
    await expect(popover).toContainText(/Context\s*12\.\dk\s*\/\s*128\.0k/)
  })
})
