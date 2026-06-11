import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from './reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest.describe('Reasonix Basic Chat', () => {
  reasonixTest('send message and receive response', async ({ authenticatedReasonixWorkspace, page }) => {
    void authenticatedReasonixWorkspace
    await sendMessage(page, ARITHMETIC_PROMPT)
    await waitForAgentIdle(page, 120_000)

    // Reasonix (DeepSeek) streams reasoning before the answer and ends the turn
    // with a "Turn ended" divider — itself an agent-role bubble — so the answer
    // is not necessarily the LAST assistant bubble; expectAssistantAnswer scans
    // every assistant bubble.
    await expectAssistantAnswer(page)
  })
})
