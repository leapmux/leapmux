import { ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

// The component tests cover the indicator's visibility transitions.
// One real turn checks provider delivery and the final browser state.
opencodeTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedOpencodeWorkspace, page }) => {
  void authenticatedOpencodeWorkspace
  await sendMessage(page, ARITHMETIC_PROMPT)
  await waitForAgentIdle(page, 120_000)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})
