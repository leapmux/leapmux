import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

// The component tests cover the indicator's visibility transitions.
// One real turn checks provider delivery and the final browser state.
piTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedPiWorkspace, page }) => {
  void authenticatedPiWorkspace
  await sendMessage(page, ARITHMETIC_PROMPT)
  await waitForAgentIdle(page, 180_000)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})
