import { expect } from '@playwright/test'
import { SCRIPTED_CONTEXT_USAGE } from '../helpers/contextUsage'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

/**
 * The native usage event must reach the agent info card. The test checks the reported count and its display.
 *
 * The Worker drives `omp --mode rpc-ui` through its JSON Lines protocol.
 */
ohMyPiTest.describe('Oh My Pi basic chat', () => {
  ohMyPiTest('reports model usage in the agent info card', async ({ native }) => {
    const { page, modelScript } = native
    const step = await modelScript.queue({ text: 'Usage recorded.', usage: { ...SCRIPTED_CONTEXT_USAGE } })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    await modelScript.waitForSteps(step + 1)
    await waitForAgentIdle(page)
    // OMP adds its own token estimate for the post-answer context tail, so the card states 12.xk, not the exact
    // total that `expectContextUsage` requires.
    const popover = await openAgentInfoCard(page)
    await expect(popover).toContainText(/Context\s*12\.\dk\s*\/\s*128\.0k/)
  })
})
