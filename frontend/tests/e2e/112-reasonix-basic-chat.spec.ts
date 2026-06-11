import { assistantBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest.describe('Reasonix Basic Chat', () => {
  reasonixTest('send message and receive response', async ({ authenticatedReasonixWorkspace, page }) => {
    void authenticatedReasonixWorkspace
    await sendMessage(page, 'What is 2+2? Reply with just the number.')
    await waitForAgentIdle(page, 120_000)

    // Reasonix (DeepSeek) streams reasoning before the answer and ends the turn
    // with a "Turn ended" divider — itself an agent-role bubble — so the answer
    // is not necessarily the LAST assistant bubble. Assert it appears somewhere
    // in the agent's rendered output.
    const texts = await assistantBubbles(page).allInnerTexts()
    expect(texts.join('\n')).toContain('4')
  })
})
