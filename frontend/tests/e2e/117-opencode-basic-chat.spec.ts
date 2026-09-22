import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

// The component tests cover the indicator's visibility transitions.
// One scripted turn checks provider delivery and the final browser state.
opencodeTest('renders an assistant answer and clears the thinking indicator', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await waitForAgentIdle(page, 120_000)
  await expectAssistantAnswer(page)
  await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
})
